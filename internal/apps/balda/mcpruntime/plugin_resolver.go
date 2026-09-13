package mcpruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const defaultMaxMCPConfigBytes = 1 << 20
const pluginMCPSchemaV1 = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"

// RevisionRootResolver resolves only retained exact source bytes.
type RevisionRootResolver interface {
	ResolveRevisionRoot(ctx context.Context, descriptor runtimecatalogcmd.SourceDescriptor) (string, error)
}

// PluginDataResolver resolves stable writable data owned by one plugin identity.
type PluginDataResolver interface {
	PluginDataRoot(ctx context.Context, source runtimecatalogcmd.SourceID) (string, error)
}

// SecretResolver resolves an allowlisted host credential handle at launch time.
type SecretResolver interface {
	ResolveSecret(ctx context.Context, handle string) (string, error)
}

// PluginPolicy is host-owned launch policy.
type PluginPolicy struct {
	AllowedEnvKeys    map[string]struct{}
	AllowedCommands   map[string]struct{}
	AllowedTransports map[string]struct{}
	MaxConfigBytes    int64
	MaxArgs           int
	MaxEnv            int
	MaxHeaders        int
}

// DefaultPluginPolicy returns conservative launch limits and no portable env allowance.
func DefaultPluginPolicy() PluginPolicy {
	return PluginPolicy{
		AllowedTransports: map[string]struct{}{"stdio": {}, "streamable-http": {}},
		MaxConfigBytes:    defaultMaxMCPConfigBytes, MaxArgs: 128, MaxEnv: 64, MaxHeaders: 64,
	}
}

// PluginResolver safely resolves validated plugin mcp.json at launch time.
type PluginResolver struct {
	roots   RevisionRootResolver
	data    PluginDataResolver
	secrets SecretResolver
	policy  PluginPolicy
}

// NewPluginResolver creates a plugin MCP launch resolver.
func NewPluginResolver(roots RevisionRootResolver, data PluginDataResolver, secrets SecretResolver, policy PluginPolicy) (*PluginResolver, error) {
	if roots == nil || data == nil {
		return nil, errors.New("plugin revision and data resolvers are required")
	}
	if policy.MaxConfigBytes == 0 && policy.MaxArgs == 0 && policy.MaxEnv == 0 && policy.MaxHeaders == 0 {
		allowed := policy.AllowedEnvKeys
		commands := policy.AllowedCommands
		transports := policy.AllowedTransports
		policy = DefaultPluginPolicy()
		policy.AllowedEnvKeys = allowed
		policy.AllowedCommands = commands
		if transports != nil {
			policy.AllowedTransports = transports
		}
	}
	if policy.MaxConfigBytes <= 0 || policy.MaxArgs <= 0 || policy.MaxEnv <= 0 || policy.MaxHeaders <= 0 {
		return nil, errors.New("plugin MCP limits must be positive")
	}
	policy.AllowedEnvKeys = cloneSet(policy.AllowedEnvKeys)
	policy.AllowedCommands = cloneSet(policy.AllowedCommands)
	policy.AllowedTransports = cloneSet(policy.AllowedTransports)
	return &PluginResolver{roots: roots, data: data, secrets: secrets, policy: policy}, nil
}

type portableMCPFile struct {
	Schema  string                       `json:"$schema"`
	Servers map[string]portableMCPServer `json:"mcpServers"`
}

type portableMCPServer struct {
	Type    string            `json:"type"`
	Command string            `json:"command"`
	Args    []string          `json:"args"`
	Env     map[string]string `json:"env"`
	CWD     string            `json:"cwd"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
}

// ResolveLaunch resolves paths and secret handles without inheriting host environment.
func (r *PluginResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindPlugin || descriptor.ConfigRef != "mcp.json" || descriptor.Name == "" {
		return LaunchConfig{}, errors.New("plugin MCP descriptor is invalid")
	}
	root, err := r.roots.ResolveRevisionRoot(ctx, runtimecatalogcmd.SourceDescriptor{ID: descriptor.ID.Source, Revision: descriptor.Revision})
	if err != nil {
		return LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	root, err = canonicalDirectory(root)
	if err != nil {
		return LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	dataRoot, err := r.data.PluginDataRoot(ctx, descriptor.ID.Source)
	if err != nil {
		return LaunchConfig{}, errors.New("plugin data is unavailable")
	}
	dataRoot, err = canonicalDirectory(dataRoot)
	if err != nil {
		return LaunchConfig{}, errors.New("plugin data is unavailable")
	}
	server, err := r.readServer(root, descriptor.Name)
	if err != nil {
		return LaunchConfig{}, err
	}
	if server.Type != descriptor.Transport {
		return LaunchConfig{}, errors.New("plugin MCP transport changed")
	}
	if _, ok := r.policy.AllowedTransports[server.Type]; !ok {
		return LaunchConfig{}, errors.New("plugin MCP transport is not allowed")
	}
	return r.resolveServer(ctx, root, dataRoot, server)
}

func (r *PluginResolver) readServer(root, name string) (portableMCPServer, error) {
	secureRoot, err := os.OpenRoot(root)
	if err != nil {
		return portableMCPServer{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	defer func() { _ = secureRoot.Close() }()
	file, err := secureRoot.Open("mcp.json")
	if err != nil {
		return portableMCPServer{}, errors.New("plugin MCP config is unavailable")
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, r.policy.MaxConfigBytes+1))
	if err != nil || int64(len(data)) > r.policy.MaxConfigBytes {
		return portableMCPServer{}, errors.New("plugin MCP config exceeds limit")
	}
	var config portableMCPFile
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&config); err != nil {
		return portableMCPServer{}, errors.New("plugin MCP config is invalid")
	}
	if config.Schema != pluginMCPSchemaV1 {
		return portableMCPServer{}, errors.New("plugin MCP schema is unsupported")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return portableMCPServer{}, errors.New("plugin MCP config has trailing data")
	}
	server, ok := config.Servers[name]
	if !ok {
		return portableMCPServer{}, errors.New("plugin MCP server is unavailable")
	}
	return server, nil
}

func (r *PluginResolver) resolveServer(ctx context.Context, root, dataRoot string, server portableMCPServer) (LaunchConfig, error) {
	config := LaunchConfig{Transport: server.Type}
	switch server.Type {
	case "stdio":
		if len(server.Args) > r.policy.MaxArgs || len(server.Env) > r.policy.MaxEnv {
			return LaunchConfig{}, errors.New("plugin MCP process config exceeds limit")
		}
		command, err := r.resolveCommand(root, server.Command)
		if err != nil {
			return LaunchConfig{}, err
		}
		config.Command = command
		for _, arg := range server.Args {
			resolved, err := expandPortablePath(arg, root, dataRoot)
			if err != nil {
				return LaunchConfig{}, errors.New("plugin MCP argument is invalid")
			}
			config.Args = append(config.Args, resolved)
		}
		config.WorkingDir, err = resolveWorkingDir(root, dataRoot, server.CWD)
		if err != nil {
			return LaunchConfig{}, err
		}
		config.Env = map[string]string{"PLUGIN_ROOT": root, "PLUGIN_DATA": dataRoot}
		for key, value := range server.Env {
			if _, ok := r.policy.AllowedEnvKeys[key]; !ok || key == "PLUGIN_ROOT" || key == "PLUGIN_DATA" {
				continue
			}
			resolved, err := r.resolveSecret(ctx, value)
			if err != nil {
				return LaunchConfig{}, err
			}
			config.Env[key] = resolved
		}
	case "streamable-http", "sse":
		if len(server.Headers) > r.policy.MaxHeaders {
			return LaunchConfig{}, errors.New("plugin MCP headers exceed limit")
		}
		config.URL = server.URL
		config.Headers = make(map[string]string, len(server.Headers))
		for key, value := range server.Headers {
			resolved, err := r.resolveSecret(ctx, value)
			if err != nil {
				return LaunchConfig{}, err
			}
			config.Headers[key] = resolved
		}
	default:
		return LaunchConfig{}, errors.New("plugin MCP transport is unsupported")
	}
	return config, nil
}

func (r *PluginResolver) resolveSecret(ctx context.Context, value string) (string, error) {
	const prefix = "${BALDA_SECRET:"
	if !strings.HasPrefix(value, prefix) {
		return value, nil
	}
	if !strings.HasSuffix(value, "}") || strings.Count(value, prefix) != 1 {
		return "", errors.New("plugin MCP secret handle is invalid")
	}
	if r.secrets == nil {
		return "", errors.New("plugin MCP secret is unavailable")
	}
	handle := strings.TrimSuffix(strings.TrimPrefix(value, prefix), "}")
	if handle == "" {
		return "", errors.New("plugin MCP secret handle is invalid")
	}
	secret, err := r.secrets.ResolveSecret(ctx, handle)
	if err != nil {
		return "", errors.New("plugin MCP secret is unavailable")
	}
	return secret, nil
}

func (r *PluginResolver) resolveCommand(root, command string) (string, error) {
	if strings.HasPrefix(command, "./") {
		path, err := containedPath(root, strings.TrimPrefix(command, "./"))
		if err != nil {
			return "", errors.New("plugin MCP command escapes revision")
		}
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
			return "", errors.New("plugin MCP command is not executable")
		}
		return path, nil
	}
	if command == "" || strings.ContainsAny(command, "/\\ \t\r\n;&|`$()") {
		return "", errors.New("plugin MCP command must be one executable token")
	}
	if _, ok := r.policy.AllowedCommands[command]; !ok {
		return "", errors.New("plugin MCP command is not allowed")
	}
	return command, nil
}

func resolveWorkingDir(root, dataRoot, value string) (string, error) {
	if value == "" || value == "${PLUGIN_ROOT}" {
		return root, nil
	}
	if value == "${PLUGIN_DATA}" {
		return dataRoot, nil
	}
	resolved, err := expandPortablePath(value, root, dataRoot)
	if err != nil {
		return "", errors.New("plugin MCP working directory escapes managed roots")
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("plugin MCP working directory is unavailable")
	}
	return resolved, nil
}

func expandPortablePath(value, root, dataRoot string) (string, error) {
	for marker, base := range map[string]string{"${PLUGIN_ROOT}": root, "${PLUGIN_DATA}": dataRoot} {
		index := strings.Index(value, marker)
		if index < 0 {
			continue
		}
		if strings.Count(value, "${PLUGIN_") != 1 {
			return "", errors.New("multiple plugin path variables")
		}
		suffix := strings.TrimPrefix(value[index+len(marker):], "/")
		resolved, err := containedPath(base, suffix)
		if err != nil {
			return "", err
		}
		return value[:index] + resolved, nil
	}
	if strings.HasPrefix(value, "./") {
		return containedPath(root, strings.TrimPrefix(value, "./"))
	}
	if strings.Contains(value, "${PLUGIN_") {
		return "", errors.New("unknown plugin path variable")
	}
	return value, nil
}

func containedPath(root, relative string) (string, error) {
	if filepath.IsAbs(relative) {
		return "", errors.New("absolute child path")
	}
	clean := filepath.Clean(relative)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("path traversal")
	}
	candidate := filepath.Join(root, clean)
	existing := candidate
	var missing []string
	for {
		_, err := os.Lstat(existing)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) || existing == root {
			return "", err
		}
		missing = append(missing, filepath.Base(existing))
		existing = filepath.Dir(existing)
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil || !pathWithin(root, resolved) {
		return "", errors.New("path escapes managed root")
	}
	for index := len(missing) - 1; index >= 0; index-- {
		resolved = filepath.Join(resolved, missing[index])
	}
	return resolved, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", errors.New("path is not a directory")
	}
	return resolved, nil
}

func cloneSet(values map[string]struct{}) map[string]struct{} {
	cloned := make(map[string]struct{}, len(values))
	for value := range values {
		cloned[value] = struct{}{}
	}
	return cloned
}
