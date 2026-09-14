package runtimecatalog

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"gopkg.in/yaml.v3"
)

const (
	pluginSchemaV1             = "https://agent-plugins.org/schemas/1.0.0/plugin.schema.json"
	mcpSchemaV1                = "https://agent-plugins.org/schemas/1.0.0/mcp.schema.json"
	baldaExtension             = "dev.baldaworks.balda"
	sourceRevisionRulesVersion = "runtime-source-v1"
)

var (
	pluginNamePattern  = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9.-]*[a-z0-9])?$`)
	skillNamePattern   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$`)
	bareCommandPattern = regexp.MustCompile(`^[^/\\\s]+$`)
)

// SourceLimits bound filesystem traversal before source content is accepted.
type SourceLimits struct {
	MaxEntries     int
	MaxFiles       int
	MaxFileBytes   int64
	MaxTotalBytes  int64
	MaxDiagnostics int
}

// DefaultSourceLimits returns conservative package-discovery limits.
func DefaultSourceLimits() SourceLimits {
	return SourceLimits{MaxEntries: 4_000, MaxFiles: 2_000, MaxFileBytes: 4 << 20, MaxTotalBytes: 32 << 20, MaxDiagnostics: 256}
}

// SourceLoader validates plugin packages and configured skill roots.
type SourceLoader struct {
	limits              SourceLimits
	supportedTransports map[string]struct{}
}

// PluginPackage is one validated plugin source plus manifest display metadata.
type PluginPackage struct {
	Source      runtimecatalogcmd.Source
	Version     string
	Description string
}

// NewSourceLoader creates a bounded loader. Empty limits use safe defaults.
func NewSourceLoader(limits SourceLimits, supportedMCPTransports ...string) (*SourceLoader, error) {
	if limits == (SourceLimits{}) {
		limits = DefaultSourceLimits()
	}
	if limits.MaxEntries <= 0 || limits.MaxFiles <= 0 || limits.MaxFileBytes <= 0 || limits.MaxTotalBytes <= 0 || limits.MaxDiagnostics <= 0 {
		return nil, errors.New("source limits must be positive")
	}
	transports := make(map[string]struct{}, len(supportedMCPTransports))
	for _, transport := range supportedMCPTransports {
		transport = strings.TrimSpace(transport)
		if transport != "stdio" && transport != "streamable-http" && transport != "sse" {
			return nil, fmt.Errorf("unsupported MCP transport option %q", transport)
		}
		transports[transport] = struct{}{}
	}
	if len(transports) == 0 {
		transports["stdio"] = struct{}{}
		transports["streamable-http"] = struct{}{}
	}
	return &SourceLoader{limits: limits, supportedTransports: transports}, nil
}

// LoadPlugin validates one Agent Plugins package and projects its components.
func (l *SourceLoader) LoadPlugin(root string) (runtimecatalogcmd.Source, error) {
	pluginPackage, err := l.LoadPluginPackage(root)
	return pluginPackage.Source, err
}

// LoadPluginPackage validates one Agent Plugins package and returns its
// catalog source together with display-only manifest metadata.
func (l *SourceLoader) LoadPluginPackage(root string) (PluginPackage, error) {
	revision, tree, err := l.captureTree(root)
	if err != nil {
		return PluginPackage{}, err
	}
	source, err := l.loadCapturedPlugin(revision, tree)
	if err != nil {
		return PluginPackage{}, err
	}
	manifest, _, _, err := validatePluginManifest(tree.files["plugin.json"])
	if err != nil {
		return PluginPackage{}, err
	}
	return PluginPackage{Source: source, Version: manifest.Version, Description: manifest.Description}, nil
}

// InspectRevision returns the bounded content identity without projecting components.
func (l *SourceLoader) InspectRevision(root string) (runtimecatalogcmd.RevisionID, error) {
	revision, _, err := l.captureTree(root)
	return revision, err
}

// MaterializePlugin validates one package capture and writes that exact revision to dest.
func (l *SourceLoader) MaterializePlugin(root, dest string) (PluginPackage, error) {
	revision, tree, err := l.captureTree(root)
	if err != nil {
		return PluginPackage{}, err
	}
	return l.materializeCapturedPlugin(revision, tree, dest)
}

// MaterializePluginFromRoot captures a plugin through an already anchored root.
// Renaming or replacing the root's lexical path cannot redirect the capture.
func (l *SourceLoader) MaterializePluginFromRoot(root *os.Root, relative, dest string) (PluginPackage, error) {
	pluginRoot, err := root.OpenRoot(filepath.ToSlash(relative))
	if err != nil {
		return PluginPackage{}, fmt.Errorf("open plugin package root: %w", err)
	}
	defer func() { _ = pluginRoot.Close() }()
	revision, tree, err := l.captureOpenRoot(pluginRoot)
	if err != nil {
		return PluginPackage{}, err
	}
	return l.materializeCapturedPlugin(revision, tree, dest)
}

func (l *SourceLoader) materializeCapturedPlugin(revision runtimecatalogcmd.RevisionID, tree capturedTree, dest string) (PluginPackage, error) {
	source, err := l.loadCapturedPlugin(revision, tree)
	if err != nil {
		return PluginPackage{}, err
	}
	if len(tree.issues) != 0 {
		return PluginPackage{}, errors.New("plugin package contains unsafe filesystem entries")
	}
	if err := materializeCapturedTree(dest, tree); err != nil {
		return PluginPackage{}, err
	}
	materialized, err := l.LoadPlugin(dest)
	if err != nil {
		return PluginPackage{}, fmt.Errorf("verify materialized plugin: %w", err)
	}
	if materialized.Descriptor.Revision != revision {
		return PluginPackage{}, errors.New("materialized plugin revision mismatch")
	}
	manifest, _, _, err := validatePluginManifest(tree.files["plugin.json"])
	if err != nil {
		return PluginPackage{}, err
	}
	return PluginPackage{Source: source, Version: manifest.Version, Description: manifest.Description}, nil
}

func (l *SourceLoader) loadCapturedPlugin(revision runtimecatalogcmd.RevisionID, tree capturedTree) (runtimecatalogcmd.Source, error) {
	manifestData, ok := tree.files["plugin.json"]
	if !ok {
		return runtimecatalogcmd.Source{}, errors.New("plugin manifest is not a captured regular file")
	}
	manifest, ignored, extensionRaw, err := validatePluginManifest(manifestData)
	if err != nil {
		return runtimecatalogcmd.Source{}, fmt.Errorf("validate plugin manifest: %w", err)
	}
	sourceID := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: manifest.Name}
	for _, issue := range tree.issues {
		if !pluginComponentIssue(issue.path) {
			return runtimecatalogcmd.Source{}, fmt.Errorf("validate plugin package: %s", issue.kind)
		}
	}
	source := runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: sourceID, Revision: revision}}
	for range ignored {
		source.Diagnostics = append(source.Diagnostics, diagnostic(sourceID, runtimecatalogcmd.DiagnosticSeverityWarning, runtimecatalogcmd.DiagnosticManifestFieldIgnored, nil))
	}
	source.Skills, source.Diagnostics = loadSkills(tree, "skills", source.Descriptor, source.Diagnostics)
	source.MCPServers, source.Diagnostics = l.loadMCP(tree, source.Descriptor, source.Diagnostics)
	commands, commandDiagnostics := loadBaldaCommands(extensionRaw, source.Descriptor, source.Skills)
	source.Commands = commands
	source.Diagnostics = append(source.Diagnostics, commandDiagnostics...)
	source.Diagnostics = boundDiagnostics(source.Diagnostics, l.limits.MaxDiagnostics)
	return source, nil
}

// LoadSkillSource validates one configured user or workspace skill root.
func (l *SourceLoader) LoadSkillSource(root string, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, error) {
	if id.Kind != runtimecatalogcmd.SourceKindUserSkill && id.Kind != runtimecatalogcmd.SourceKindWorkspaceSkill {
		return runtimecatalogcmd.Source{}, fmt.Errorf("source %q is not a configured skill source", id.String())
	}
	if strings.TrimSpace(id.Name) == "" || id.Name != strings.TrimSpace(id.Name) {
		return runtimecatalogcmd.Source{}, errors.New("normalized skill source name is required")
	}
	revision, tree, err := l.captureTree(root)
	if err != nil {
		return runtimecatalogcmd.Source{}, err
	}
	descriptor := runtimecatalogcmd.SourceDescriptor{ID: id, Revision: revision}
	source := runtimecatalogcmd.Source{Descriptor: descriptor}
	source.Skills, source.Diagnostics = loadSkills(tree, ".", descriptor, nil)
	source.Diagnostics = boundDiagnostics(source.Diagnostics, l.limits.MaxDiagnostics)
	return source, nil
}

type pluginManifest struct {
	Name        string
	Version     string
	Description string
}

func validatePluginManifest(data []byte) (pluginManifest, []string, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSONObject(data, &fields); err != nil {
		return pluginManifest{}, nil, nil, err
	}
	allowed := map[string]struct{}{"$schema": {}, "name": {}, "version": {}, "description": {}, "author": {}, "homepage": {}, "repository": {}, "license": {}, "keywords": {}, "extensions": {}}
	var ignored []string
	for key := range fields {
		if _, ok := allowed[key]; !ok {
			ignored = append(ignored, key)
		}
	}
	sort.Strings(ignored)
	var schema, name string
	if err := decodeRequired(fields, "$schema", &schema); err != nil || schema != pluginSchemaV1 {
		return pluginManifest{}, nil, nil, fmt.Errorf("unsupported $schema")
	}
	if err := decodeRequired(fields, "name", &name); err != nil || !validPluginName(name) {
		return pluginManifest{}, nil, nil, fmt.Errorf("invalid plugin name")
	}
	values := make(map[string]string)
	for _, key := range []string{"version", "description", "homepage", "repository", "license"} {
		if raw, ok := fields[key]; ok {
			var value string
			if err := json.Unmarshal(raw, &value); err != nil {
				return pluginManifest{}, nil, nil, fmt.Errorf("field %q must be a string", key)
			}
			values[key] = value
		}
	}
	if raw, ok := fields["keywords"]; ok {
		var value []string
		if err := json.Unmarshal(raw, &value); err != nil {
			return pluginManifest{}, nil, nil, errors.New("field \"keywords\" must be a string array")
		}
	}
	if raw, ok := fields["author"]; ok {
		var author map[string]json.RawMessage
		if err := decodeJSONObject(raw, &author); err != nil {
			return pluginManifest{}, nil, nil, errors.New("field \"author\" must be an object")
		}
		for key, value := range author {
			if key != "name" && key != "email" && key != "url" {
				return pluginManifest{}, nil, nil, fmt.Errorf("unknown author field %q", key)
			}
			var text string
			if err := json.Unmarshal(value, &text); err != nil {
				return pluginManifest{}, nil, nil, fmt.Errorf("author field %q must be a string", key)
			}
		}
	}
	var extensionRaw json.RawMessage
	if raw, ok := fields["extensions"]; ok {
		var extensions map[string]json.RawMessage
		if err := decodeJSONObject(raw, &extensions); err == nil {
			extensionRaw = extensions[baldaExtension]
		} else {
			ignored = append(ignored, "extensions")
		}
	}
	return pluginManifest{Name: name, Version: values["version"], Description: values["description"]}, ignored, extensionRaw, nil
}

func validPluginName(name string) bool {
	return len(name) <= 64 && pluginNamePattern.MatchString(name) && !strings.Contains(name, "--") && !strings.Contains(name, "..")
}

func loadSkills(tree capturedTree, relativeRoot string, source runtimecatalogcmd.SourceDescriptor, diagnostics []runtimecatalogcmd.Diagnostic) ([]runtimecatalogcmd.SkillMetadata, []runtimecatalogcmd.Diagnostic) {
	rootKey := cleanTreePath(relativeRoot)
	if rootKey != "." && !tree.dirs[rootKey] {
		if hasTreeIssue(tree.issues, relativeRoot) {
			return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillComponentInvalid, nil))
		}
		if _, present := tree.files[rootKey]; present {
			return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillComponentInvalid, nil))
		}
		return nil, diagnostics
	}
	if hasTreeIssue(tree.issues, relativeRoot) {
		return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillComponentInvalid, nil))
	}
	names := tree.immediateDirectories(rootKey)
	for _, issue := range tree.issues {
		if name := issueSkillName(rootKey, issue.path); name != "" {
			names = append(names, name)
		}
	}
	names = uniqueSorted(names)
	var skills []runtimecatalogcmd.SkillMetadata
	for _, name := range names {
		if hasSkillTreeIssue(tree.issues, relativeRoot, name) {
			id := runtimecatalogcmd.ContributionID{Source: source.ID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
			diagnostics = append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillInvalid, &id))
			continue
		}
		resource := cleanTreePath(filepath.Join(relativeRoot, name, "SKILL.md"))
		data, ok := tree.files[resource]
		if !ok {
			if tree.dirs[resource] {
				id := runtimecatalogcmd.ContributionID{Source: source.ID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
				diagnostics = append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillInvalid, &id))
			}
			continue
		}
		parsedName, description, err := parseSkillFrontmatter(data, name)
		id := runtimecatalogcmd.ContributionID{Source: source.ID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
		if err != nil {
			diagnostics = append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticSkillInvalid, &id))
			continue
		}
		id.Name = parsedName
		skills = append(skills, runtimecatalogcmd.SkillMetadata{ID: id, Revision: source.Revision, Name: parsedName, Description: description, Resource: resource})
	}
	return skills, diagnostics
}

type skillFrontmatter struct {
	Name          string            `yaml:"name"`
	Description   string            `yaml:"description"`
	License       string            `yaml:"license"`
	Compatibility string            `yaml:"compatibility"`
	Metadata      map[string]string `yaml:"metadata"`
	AllowedTools  string            `yaml:"allowed-tools"`
}

func parseSkillFrontmatter(data []byte, directory string) (string, string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 1024), len(data)+1)
	if !scanner.Scan() || strings.TrimSpace(scanner.Text()) != "---" {
		return "", "", errors.New("missing YAML frontmatter")
	}
	var yamlLines []string
	closed := false
	for scanner.Scan() {
		if strings.TrimSpace(scanner.Text()) == "---" {
			closed = true
			break
		}
		yamlLines = append(yamlLines, scanner.Text())
	}
	if !closed || scanner.Err() != nil {
		return "", "", errors.New("unterminated YAML frontmatter")
	}
	var metadata skillFrontmatter
	decoder := yaml.NewDecoder(strings.NewReader(strings.Join(yamlLines, "\n")))
	decoder.KnownFields(true)
	if err := decoder.Decode(&metadata); err != nil {
		return "", "", fmt.Errorf("decode YAML frontmatter: %w", err)
	}
	if metadata.Name != directory || len(metadata.Name) > 64 || !skillNamePattern.MatchString(metadata.Name) || strings.Contains(metadata.Name, "--") {
		return "", "", errors.New("invalid skill name")
	}
	if metadata.Description == "" || len(metadata.Description) > 1024 {
		return "", "", errors.New("invalid skill description")
	}
	if metadata.Compatibility != "" && len(metadata.Compatibility) > 500 {
		return "", "", errors.New("invalid skill compatibility")
	}
	return metadata.Name, metadata.Description, nil
}

func (l *SourceLoader) loadMCP(tree capturedTree, source runtimecatalogcmd.SourceDescriptor, diagnostics []runtimecatalogcmd.Diagnostic) ([]runtimecatalogcmd.MCPServerDescriptor, []runtimecatalogcmd.Diagnostic) {
	if hasTreeIssuePrefix(tree.issues, "mcp.json") || tree.dirs["mcp.json"] {
		return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticMCPConfigInvalid, nil))
	}
	data, ok := tree.files["mcp.json"]
	if !ok {
		return nil, diagnostics
	}
	var fields map[string]json.RawMessage
	if err := decodeJSONObject(data, &fields); err != nil || len(fields) != 2 {
		return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticMCPConfigInvalid, nil))
	}
	var schema string
	var servers map[string]json.RawMessage
	if decodeRequired(fields, "$schema", &schema) != nil || schema != mcpSchemaV1 || decodeRequired(fields, "mcpServers", &servers) != nil {
		return nil, append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticMCPConfigInvalid, nil))
	}
	names := make([]string, 0, len(servers))
	for name := range servers {
		names = append(names, name)
	}
	sort.Strings(names)
	var descriptors []runtimecatalogcmd.MCPServerDescriptor
	for _, name := range names {
		transport, err := validateMCPServer(tree, servers[name])
		id := runtimecatalogcmd.ContributionID{Source: source.ID, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: name}
		if err != nil || strings.TrimSpace(name) == "" {
			diagnostics = append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticMCPServerInvalid, &id))
			continue
		}
		if _, ok := l.supportedTransports[transport]; !ok {
			diagnostics = append(diagnostics, diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityWarning, runtimecatalogcmd.DiagnosticMCPTransportUnsupported, &id))
			continue
		}
		descriptors = append(descriptors, runtimecatalogcmd.MCPServerDescriptor{ID: id, Revision: source.Revision, Name: name, Transport: transport, ConfigRef: "mcp.json"})
	}
	return descriptors, diagnostics
}

func validateMCPServer(tree capturedTree, raw json.RawMessage) (string, error) {
	var fields map[string]json.RawMessage
	if err := decodeJSONObject(raw, &fields); err != nil {
		return "", err
	}
	var transport string
	if err := decodeRequired(fields, "type", &transport); err != nil {
		return "", err
	}
	switch transport {
	case "stdio":
		if !onlyFields(fields, "type", "command", "args", "env", "cwd") {
			return "", errors.New("unknown stdio field")
		}
		var command string
		if decodeRequired(fields, "command", &command) != nil || !validCommand(tree, command) {
			return "", errors.New("invalid command")
		}
		if rawArgs, ok := fields["args"]; ok {
			var args []string
			if json.Unmarshal(rawArgs, &args) != nil {
				return "", errors.New("invalid args")
			}
		}
		if rawEnv, ok := fields["env"]; ok {
			var env map[string]string
			if json.Unmarshal(rawEnv, &env) != nil {
				return "", errors.New("invalid env")
			}
			if _, ok := env["PLUGIN_ROOT"]; ok {
				return "", errors.New("reserved env")
			}
			if _, ok := env["PLUGIN_DATA"]; ok {
				return "", errors.New("reserved env")
			}
		}
		if rawCWD, ok := fields["cwd"]; ok {
			var cwd string
			if json.Unmarshal(rawCWD, &cwd) != nil || !validCWD(tree, cwd) {
				return "", errors.New("invalid cwd")
			}
		}
	case "streamable-http", "sse":
		if !onlyFields(fields, "type", "url", "headers") {
			return "", errors.New("unknown HTTP field")
		}
		var address string
		if decodeRequired(fields, "url", &address) != nil || !validMCPURL(address) {
			return "", errors.New("invalid URL")
		}
		if rawHeaders, ok := fields["headers"]; ok {
			var headers map[string]string
			if json.Unmarshal(rawHeaders, &headers) != nil || !validHeaders(headers) {
				return "", errors.New("invalid headers")
			}
		}
	default:
		return "", errors.New("unknown transport")
	}
	return transport, nil
}

func validCommand(tree capturedTree, command string) bool {
	if bareCommandPattern.MatchString(command) {
		return true
	}
	if !strings.HasPrefix(command, "./") || strings.ContainsAny(command, "\t\r\n") {
		return false
	}
	_, ok := tree.files[cleanTreePath(command)]
	return ok
}

func validCWD(tree capturedTree, cwd string) bool {
	if strings.HasPrefix(cwd, "./") {
		return tree.dirs[cleanTreePath(cwd)]
	}
	for _, prefix := range []string{"${PLUGIN_ROOT}", "${PLUGIN_DATA}"} {
		if cwd == prefix {
			return true
		}
		if strings.HasPrefix(cwd, prefix+"/") {
			remaining := strings.TrimPrefix(cwd, prefix+"/")
			if remaining == "" || filepath.Clean(remaining) == ".." || strings.HasPrefix(filepath.Clean(remaining), ".."+string(filepath.Separator)) {
				return false
			}
			if prefix == "${PLUGIN_ROOT}" {
				return tree.dirs[cleanTreePath(remaining)]
			}
			return true
		}
	}
	return false
}

func validMCPURL(address string) bool {
	parsed, err := url.Parse(address)
	if err != nil || parsed.User != nil || parsed.Fragment != "" || parsed.Hostname() == "" {
		return false
	}
	if parsed.Scheme == "https" {
		return true
	}
	if parsed.Scheme != "http" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func validHeaders(headers map[string]string) bool {
	seen := make(map[string]struct{}, len(headers))
	for name, value := range headers {
		lower := strings.ToLower(name)
		if _, ok := seen[lower]; ok || !validHeaderName(name) || !validHeaderValue(value) {
			return false
		}
		seen[lower] = struct{}{}
	}
	return true
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for _, char := range name {
		if !strings.ContainsRune("!#$%&'*+-.^_`|~0123456789abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", char) {
			return false
		}
	}
	return true
}

func validHeaderValue(value string) bool { return httpgutsValidHeaderValue(value) }

// Keep header validation local so runtime discovery does not depend on internal net/http packages.
func httpgutsValidHeaderValue(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\r' || value[i] == '\n' || value[i] == 0x7f || (value[i] < 0x20 && value[i] != '\t') {
			return false
		}
	}
	return true
}

type baldaCommandExtension struct {
	SchemaVersion int               `json:"schema_version"`
	Commands      []json.RawMessage `json:"commands"`
}

func loadBaldaCommands(raw json.RawMessage, source runtimecatalogcmd.SourceDescriptor, skills []runtimecatalogcmd.SkillMetadata) ([]runtimecatalogcmd.CommandDescriptor, []runtimecatalogcmd.Diagnostic) {
	if len(raw) == 0 {
		return nil, nil
	}
	var fields map[string]json.RawMessage
	if decodeJSONObject(raw, &fields) != nil || !onlyFields(fields, "schema_version", "commands") {
		return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
	}
	var extension baldaCommandExtension
	if json.Unmarshal(raw, &extension) != nil || extension.SchemaVersion != 1 || extension.Commands == nil || len(extension.Commands) > 128 {
		return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
	}
	availableSkills := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		availableSkills[skill.Name] = struct{}{}
	}
	seen := make(map[string]struct{}, len(extension.Commands))
	commands := make([]runtimecatalogcmd.CommandDescriptor, 0, len(extension.Commands))
	for _, rawCommand := range extension.Commands {
		var fields map[string]json.RawMessage
		if decodeJSONObject(rawCommand, &fields) != nil || !onlyFields(fields, "name", "description", "skill") || len(fields) != 3 {
			return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
		}
		var command struct{ Name, Description, Skill string }
		if decodeRequired(fields, "name", &command.Name) != nil || decodeRequired(fields, "description", &command.Description) != nil || decodeRequired(fields, "skill", &command.Skill) != nil {
			return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
		}
		name := strings.ToLower(strings.TrimSpace(command.Name))
		description := strings.TrimSpace(command.Description)
		if name == "" || len(name) > 64 || command.Name != name || !skillNamePattern.MatchString(name) || strings.Contains(name, "--") || description == "" || len(description) > 1024 || strings.TrimSpace(command.Skill) != command.Skill {
			return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
		}
		if _, ok := seen[name]; ok {
			return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
		}
		if _, ok := availableSkills[command.Skill]; !ok {
			return nil, []runtimecatalogcmd.Diagnostic{diagnostic(source.ID, runtimecatalogcmd.DiagnosticSeverityError, runtimecatalogcmd.DiagnosticExtensionInvalid, nil)}
		}
		seen[name] = struct{}{}
		id := runtimecatalogcmd.ContributionID{Source: source.ID, Kind: runtimecatalogcmd.ContributionKindCommand, Name: name}
		commands = append(commands, runtimecatalogcmd.CommandDescriptor{ID: id, Revision: source.Revision, Name: name, Description: description, Skill: &runtimecatalogcmd.SkillRef{Source: source.ID, Revision: source.Revision, Name: command.Skill}, Advertised: true})
	}
	return commands, nil
}

type treeIssue struct {
	path string
	kind string
}

type capturedTree struct {
	files  map[string][]byte
	dirs   map[string]bool
	links  map[string]string
	modes  map[string]fs.FileMode
	issues []treeIssue
}

func (l *SourceLoader) captureTree(root string) (runtimecatalogcmd.RevisionID, capturedTree, error) {
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", capturedTree{}, fmt.Errorf("resolve source root: %w", err)
	}
	resolvedRoot, err = filepath.Abs(resolvedRoot)
	if err != nil {
		return "", capturedTree{}, fmt.Errorf("make source root absolute: %w", err)
	}
	info, err := os.Stat(resolvedRoot)
	if err != nil || !info.IsDir() {
		return "", capturedTree{}, errors.New("source root must be a directory")
	}
	secureRoot, err := os.OpenRoot(resolvedRoot)
	if err != nil {
		return "", capturedTree{}, fmt.Errorf("open source root: %w", err)
	}
	defer func() { _ = secureRoot.Close() }()
	return l.captureOpenRoot(secureRoot)
}

func (l *SourceLoader) captureOpenRoot(secureRoot *os.Root) (runtimecatalogcmd.RevisionID, capturedTree, error) {
	tree := capturedTree{
		files: make(map[string][]byte), dirs: map[string]bool{".": true},
		links: make(map[string]string), modes: make(map[string]fs.FileMode),
	}
	hash := sha256.New()
	_, _ = io.WriteString(hash, sourceRevisionRulesVersion)
	_, _ = hash.Write([]byte{0})
	entries, files, total := 0, 0, int64(0)
	err := fs.WalkDir(secureRoot.FS(), ".", func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == "." {
			return nil
		}
		relative := filepath.ToSlash(path)
		entries++
		if entries > l.limits.MaxEntries {
			return errors.New("source entry-count limit exceeded")
		}
		if entry.IsDir() {
			tree.dirs[relative] = true
			_, _ = io.WriteString(hash, relative)
			_, _ = io.WriteString(hash, "\x00directory\x00")
			return nil
		}
		files++
		if files > l.limits.MaxFiles {
			return errors.New("source file-count limit exceeded")
		}
		modeClass := "regular"
		if entry.Type()&os.ModeSymlink != 0 {
			modeClass = "symlink"
			target, err := secureRoot.Readlink(relative)
			if err != nil {
				return err
			}
			_, _ = io.WriteString(hash, target)
			_, _ = hash.Write([]byte{0})
			tree.links[relative] = target
			if _, walkErr = secureRoot.Stat(relative); walkErr != nil {
				tree.issues = append(tree.issues, treeIssue{path: relative, kind: "unresolvable symlink"})
				return hashTreeIssue(hash, relative, "unresolvable-symlink")
			}
		}
		fileInfo, walkErr := secureRoot.Stat(relative)
		if walkErr != nil {
			tree.issues = append(tree.issues, treeIssue{path: relative, kind: "unreadable entry"})
			return hashTreeIssue(hash, relative, "unreadable-entry")
		}
		if !fileInfo.Mode().IsRegular() {
			tree.issues = append(tree.issues, treeIssue{path: relative, kind: "special file"})
			return hashTreeIssue(hash, relative, "special-file")
		}
		if modeClass == "regular" && fileInfo.Mode().Perm()&0o111 != 0 {
			modeClass = "executable"
		}
		data, walkErr := readRootFile(secureRoot, relative, l.limits.MaxFileBytes)
		if walkErr != nil {
			return walkErr
		}
		tree.files[relative] = data
		tree.modes[relative] = fileInfo.Mode().Perm()
		total += int64(len(data))
		if total > l.limits.MaxTotalBytes {
			return errors.New("source total-size limit exceeded")
		}
		_, _ = io.WriteString(hash, relative)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, modeClass)
		_, _ = hash.Write([]byte{0})
		_, _ = io.WriteString(hash, fmt.Sprintf("%d", len(data)))
		_, _ = hash.Write([]byte{0})
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
		return nil
	})
	if err != nil {
		return "", capturedTree{}, fmt.Errorf("capture source tree: %w", err)
	}
	return runtimecatalogcmd.RevisionID(hex.EncodeToString(hash.Sum(nil))), tree, nil
}

func materializeCapturedTree(dest string, tree capturedTree) error {
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return errors.New("revision destination already exists")
	}
	if err := os.Mkdir(dest, 0o700); err != nil {
		return fmt.Errorf("create revision destination: %w", err)
	}
	directories := make([]string, 0, len(tree.dirs))
	for directory := range tree.dirs {
		if directory != "." {
			directories = append(directories, directory)
		}
	}
	sort.Slice(directories, func(i, j int) bool {
		return len(strings.Split(directories[i], "/")) < len(strings.Split(directories[j], "/")) || (len(strings.Split(directories[i], "/")) == len(strings.Split(directories[j], "/")) && directories[i] < directories[j])
	})
	for _, directory := range directories {
		if err := os.Mkdir(filepath.Join(dest, filepath.FromSlash(directory)), 0o700); err != nil {
			return fmt.Errorf("create revision directory: %w", err)
		}
	}
	paths := make([]string, 0, len(tree.files))
	for path := range tree.files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	for _, path := range paths {
		fullPath := filepath.Join(dest, filepath.FromSlash(path))
		if target, linked := tree.links[path]; linked {
			if err := os.Symlink(target, fullPath); err != nil {
				return fmt.Errorf("create revision symlink: %w", err)
			}
			continue
		}
		mode := fs.FileMode(0o400)
		if tree.modes[path]&0o111 != 0 {
			mode = 0o500
		}
		if err := os.WriteFile(fullPath, tree.files[path], mode); err != nil {
			return fmt.Errorf("write revision file: %w", err)
		}
	}
	for i := len(directories) - 1; i >= 0; i-- {
		if err := os.Chmod(filepath.Join(dest, filepath.FromSlash(directories[i])), 0o500); err != nil {
			return fmt.Errorf("protect revision directory: %w", err)
		}
	}
	if err := os.Chmod(dest, 0o500); err != nil {
		return fmt.Errorf("protect revision root: %w", err)
	}
	return nil
}

func readRootFile(root *os.Root, relative string, maxBytes int64) ([]byte, error) {
	file, err := openRootReadFile(root, relative)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || before.Size() > maxBytes {
		return nil, errors.New("file is not a bounded regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maxBytes+1))
	if err != nil || int64(len(data)) > maxBytes {
		return nil, errors.New("file exceeds size limit")
	}
	after, err := file.Stat()
	if err != nil || after.Size() != int64(len(data)) || before.ModTime() != after.ModTime() {
		return nil, errors.New("file changed during validation")
	}
	return data, nil
}

func hashTreeIssue(hash io.Writer, relative, kind string) error {
	_, _ = io.WriteString(hash, filepath.ToSlash(relative))
	_, _ = io.WriteString(hash, "\x00invalid\x00")
	_, _ = io.WriteString(hash, kind)
	_, _ = io.WriteString(hash, "\x00")
	return nil
}

func pluginComponentIssue(path string) bool {
	path = filepath.ToSlash(path)
	return path == "mcp.json" || strings.HasPrefix(path, "mcp.json/") || path == "skills" || strings.HasPrefix(path, "skills/")
}

func hasTreeIssue(issues []treeIssue, relativeRoot string) bool {
	root := cleanTreePath(relativeRoot)
	for _, issue := range issues {
		if issue.path == root {
			return true
		}
	}
	return false
}

func hasTreeIssuePrefix(issues []treeIssue, prefix string) bool {
	prefix = cleanTreePath(prefix)
	for _, issue := range issues {
		if issue.path == prefix || strings.HasPrefix(issue.path, prefix+"/") {
			return true
		}
	}
	return false
}

func hasSkillTreeIssue(issues []treeIssue, relativeRoot, skill string) bool {
	prefix := cleanTreePath(filepath.Join(relativeRoot, skill))
	for _, issue := range issues {
		if issue.path == prefix || strings.HasPrefix(issue.path, prefix+"/") {
			return true
		}
	}
	return false
}

func (tree capturedTree) immediateDirectories(root string) []string {
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	var names []string
	for path := range tree.dirs {
		if !strings.HasPrefix(path, prefix) {
			continue
		}
		remainder := strings.TrimPrefix(path, prefix)
		if remainder != "" && remainder != "." && !strings.Contains(remainder, "/") {
			names = append(names, remainder)
		}
	}
	return names
}

func issueSkillName(root, path string) string {
	prefix := ""
	if root != "." {
		prefix = root + "/"
	}
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	remainder := strings.TrimPrefix(path, prefix)
	if remainder == "" {
		return ""
	}
	return strings.SplitN(remainder, "/", 2)[0]
}

func uniqueSorted(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	values = values[:0]
	for value := range set {
		values = append(values, value)
	}
	sort.Strings(values)
	return values
}

func cleanTreePath(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	return strings.TrimPrefix(clean, "./")
}

func boundDiagnostics(diagnostics []runtimecatalogcmd.Diagnostic, maximum int) []runtimecatalogcmd.Diagnostic {
	if len(diagnostics) <= maximum {
		return diagnostics
	}
	return diagnostics[:maximum:maximum]
}

func decodeJSONObject(data []byte, out *map[string]json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(out); err != nil || *out == nil {
		if err != nil {
			return err
		}
		return errors.New("expected JSON object")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("unexpected trailing JSON")
	}
	return nil
}

func decodeRequired(fields map[string]json.RawMessage, key string, out any) error {
	raw, ok := fields[key]
	if !ok {
		return fmt.Errorf("missing field %q", key)
	}
	return json.Unmarshal(raw, out)
}

func onlyFields(fields map[string]json.RawMessage, allowed ...string) bool {
	set := make(map[string]struct{}, len(allowed))
	for _, field := range allowed {
		set[field] = struct{}{}
	}
	for field := range fields {
		if _, ok := set[field]; !ok {
			return false
		}
	}
	return true
}

func diagnostic(source runtimecatalogcmd.SourceID, severity runtimecatalogcmd.DiagnosticSeverity, code string, contribution *runtimecatalogcmd.ContributionID) runtimecatalogcmd.Diagnostic {
	return runtimecatalogcmd.Diagnostic{Severity: severity, Code: code, Source: source, Contribution: contribution}
}
