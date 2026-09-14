// Package catalogapp wires runtime contribution sources to their consumers.
package catalogapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

const builtinRevisionSeed = "balda-builtin-commands-v1"
const snapshotKeyPrefix = "runtime_catalog_snapshot:"

// Runtime owns compilation, retention, scope overlays, and projection fanout.
type Runtime struct {
	mu            sync.Mutex
	stateDir      string
	compiler      *runtimecatalog.Compiler
	store         *runtimecatalog.Store
	loader        *runtimecatalog.SourceLoader
	archive       *runtimecatalog.RevisionArchive
	reader        *runtimecatalog.SkillReader
	plugins       baldastate.PluginStore
	sessions      baldastate.SessionStore
	kv            baldastate.KVStore
	builtin       runtimecatalogcmd.Source
	configuredMCP []runtimecatalogcmd.Source
	mcp           *mcpruntime.Reconciler
	ads           *commandfx.AdvertisementProjector
}

// NewRuntime creates the application catalog without publishing mutable state.
func NewRuntime(
	stateDir string,
	provider baldastate.Provider,
	advertisements []commandcmd.Advertisement,
	configured map[string]agentconfig.MCPServerConfig,
	registry *mcpregistry.MapRegistry,
	commands *commandcmd.Registry,
) (*Runtime, error) {
	stateDir = strings.TrimSpace(stateDir)
	if stateDir == "" || provider == nil || registry == nil || commands == nil {
		return nil, errors.New("catalog state, storage, MCP registry, and command registry are required")
	}
	loader, err := runtimecatalog.NewSourceLoader(runtimecatalog.SourceLimits{})
	if err != nil {
		return nil, err
	}
	archive, err := runtimecatalog.NewRevisionArchive(filepath.Join(stateDir, "catalog-revisions"), runtimecatalog.SourceLimits{})
	if err != nil {
		return nil, err
	}
	reader, err := runtimecatalog.NewSkillReader(archive, runtimecatalog.SkillReadLimits{})
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{
		stateDir: stateDir, compiler: runtimecatalog.NewCompiler(), store: runtimecatalog.NewStore(),
		loader: loader, archive: archive, reader: reader, plugins: provider.Plugins(), sessions: provider.Sessions(), kv: provider.AppKV(),
		builtin: builtinSource(advertisements), configuredMCP: configuredMCPSources(configured),
	}
	pluginResolver, err := mcpruntime.NewPluginResolver(archive, runtime, nil, mcpruntime.PluginPolicy{})
	if err != nil {
		return nil, err
	}
	projector, err := mcpfx.NewRegistryProjector(registry)
	if err != nil {
		return nil, err
	}
	runtime.mcp, err = mcpruntime.New(
		mcpruntime.RoutedResolver{Configured: configuredMCPResolver(configured), Plugin: pluginResolver},
		mcpfx.NewClientLauncher(), projector, mcpruntime.Limits{},
	)
	if err != nil {
		return nil, err
	}
	var targets []commandfx.AdvertisementTarget
	seenTransports := make(map[string]struct{})
	for _, advertisement := range advertisements {
		transport := strings.ToLower(strings.TrimSpace(advertisement.Transport))
		if !advertisement.Enabled || transport == "" {
			continue
		}
		if _, ok := seenTransports[transport]; ok {
			continue
		}
		seenTransports[transport] = struct{}{}
		targets = append(targets, commandfx.NewRegistryAdvertisementTarget(transport, commands))
	}
	runtime.ads, err = commandfx.NewAdvertisementProjector(commandfx.NewPinnedCommandReadiness(runtime.mcp), targets)
	if err != nil {
		return nil, err
	}
	return runtime, nil
}

// Store returns the retained immutable snapshot store.
func (r *Runtime) Store() *runtimecatalog.Store { return r.store }

// MCP returns the desired-state MCP reconciler.
func (r *Runtime) MCP() *mcpruntime.Reconciler { return r.mcp }

// Advertisements returns the dynamic transport projection service.
func (r *Runtime) Advertisements() *commandfx.AdvertisementProjector { return r.ads }

// PreparePluginCandidate compiles all host sources and enabled plugin sources.
func (r *Runtime) PreparePluginCandidate(ctx context.Context, plugins []runtimecatalogcmd.Source) (runtimecatalogcmd.Snapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	sources := []runtimecatalogcmd.Source{r.builtin}
	sources = append(sources, r.configuredMCP...)
	userSources, err := r.loadSkillSources(ctx, filepath.Join(r.stateDir, "skills"), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "default"})
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	sources = append(sources, userSources...)
	for _, source := range plugins {
		if err := r.retainPlugin(ctx, source.Descriptor); err != nil {
			return runtimecatalogcmd.Snapshot{}, err
		}
		sources = append(sources, source)
	}
	return r.compiler.CompileApplication(sources)
}

// PublishCandidate atomically makes a compiled application snapshot current,
// then updates process-local projections from that exact retained value.
func (r *Runtime) PublishCandidate(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) error {
	if err := r.persistSnapshot(ctx, snapshot); err != nil {
		return err
	}
	retained, err := r.store.PublishApplication(snapshot)
	if err != nil {
		return err
	}
	if r.mcp != nil {
		r.mcp.Reconcile(ctx, retained)
	}
	if r.ads != nil {
		if err := r.ads.Project(ctx, retained); err != nil {
			return err
		}
	}
	return nil
}

// ResolveEffectiveSnapshot pins command ingress to its trusted session scope.
func (r *Runtime) ResolveEffectiveSnapshot(ctx context.Context, request commandfx.SnapshotRequest) (runtimecatalogcmd.SnapshotID, error) {
	application, err := r.store.Application()
	if err != nil {
		return "", err
	}
	sessionID := strings.TrimSpace(request.SessionID)
	if sessionID == "" {
		return application.ID, nil
	}
	record, found, err := r.sessions.GetBySessionID(ctx, sessionID)
	if err != nil {
		return "", err
	}
	if !found || strings.TrimSpace(record.WorkspaceDir) == "" {
		return application.ID, nil
	}
	snapshot, err := r.effectiveSnapshot(ctx, record.WorkspaceDir, sessionID)
	if err != nil {
		return "", err
	}
	return snapshot.ID, nil
}

// ResolveCommandSnapshot returns only the exact snapshot pinned in a payload.
func (r *Runtime) ResolveCommandSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	return r.retainedSnapshot(ctx, id)
}

// CurrentSkillSnapshot resolves application plus the trusted workspace overlay.
func (r *Runtime) CurrentSkillSnapshot(ctx context.Context, scope baldaagent.TrustedSkillScope) (runtimecatalogcmd.Snapshot, error) {
	if strings.TrimSpace(scope.Workspace) == "" {
		return r.store.Application()
	}
	return r.effectiveSnapshot(ctx, scope.Workspace, workspaceScopeName(scope.Workspace))
}

// RetainedSkillSnapshot returns one exact immutable snapshot.
func (r *Runtime) RetainedSkillSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	return r.retainedSnapshot(ctx, id)
}

// ReadSkill delegates bounded exact-revision reads to the archive-backed reader.
func (r *Runtime) ReadSkill(ctx context.Context, request runtimecatalogcmd.SkillReadRequest) (runtimecatalogcmd.LoadedSkill, error) {
	return r.reader.ReadSkill(ctx, request)
}

// MCPServerIDs returns ready revision-qualified plugin MCP registry IDs for a
// provider runtime constructed in the trusted workspace scope.
func (r *Runtime) MCPServerIDs(ctx context.Context, workspace string) ([]string, error) {
	snapshot, err := r.CurrentSkillSnapshot(ctx, baldaagent.TrustedSkillScope{Workspace: workspace})
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, descriptor := range snapshot.MCPServers {
		if descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindPlugin || !r.mcp.MCPServerReady(descriptor.ID.Source, descriptor.Revision, descriptor.Name) {
			continue
		}
		ids = append(ids, mcpfx.RegistryID(mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}))
	}
	sort.Strings(ids)
	return ids, nil
}

// AcquireMCPServerIDs pins ready plugin MCP instances from one exact retained
// snapshot for the lifetime of a provider turn.
func (r *Runtime) AcquireMCPServerIDs(ctx context.Context, snapshotID runtimecatalogcmd.SnapshotID) ([]string, func(), error) {
	snapshot, err := r.retainedSnapshot(ctx, snapshotID)
	if err != nil {
		return nil, nil, err
	}
	descriptors := make([]runtimecatalogcmd.MCPServerDescriptor, 0, len(snapshot.MCPServers))
	for _, descriptor := range snapshot.MCPServers {
		if descriptor.ID.Source.Kind == runtimecatalogcmd.SourceKindPlugin {
			descriptors = append(descriptors, descriptor)
		}
	}
	keys, release, err := r.mcp.AcquireDescriptors(ctx, descriptors)
	if err != nil {
		return nil, nil, err
	}
	ids := make([]string, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, mcpfx.RegistryID(key))
	}
	sort.Strings(ids)
	return ids, release, nil
}

// PluginDataRoot resolves the stable writable root for a managed plugin.
func (r *Runtime) PluginDataRoot(ctx context.Context, source runtimecatalogcmd.SourceID) (string, error) {
	if source.Kind != runtimecatalogcmd.SourceKindPlugin {
		return "", errors.New("plugin source is required")
	}
	install, found, err := r.plugins.GetPluginInstall(ctx, source.Name)
	if err != nil {
		return "", err
	}
	if !found {
		return "", runtimecatalogcmd.ErrRevisionUnavailable
	}
	root := filepath.Join(r.stateDir, filepath.FromSlash(install.DataRelativePath))
	resolvedState, err := filepath.EvalSymlinks(r.stateDir)
	if err != nil {
		return "", errors.New("plugin data is unavailable")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", errors.New("plugin data is unavailable")
	}
	relative, err := filepath.Rel(resolvedState, resolvedRoot)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("plugin data is unavailable")
	}
	return resolvedRoot, nil
}

func (r *Runtime) effectiveSnapshot(ctx context.Context, workspace, scopeName string) (runtimecatalogcmd.Snapshot, error) {
	application, err := r.store.Application()
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	sources, err := r.loadSkillSources(ctx, filepath.Join(workspace, ".agents", "skills"), runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindWorkspaceSkill, Name: scopeName})
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	overlay, err := r.compiler.CompileWorkspace(scopeName, sources)
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	effective, err := r.compiler.Merge(application, overlay)
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	if err := r.persistSnapshot(ctx, effective); err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	return r.store.Publish(effective)
}

type snapshotRecord struct {
	ID          runtimecatalogcmd.SnapshotID            `json:"id"`
	Scope       runtimecatalogcmd.SnapshotScope         `json:"scope"`
	Parents     []runtimecatalogcmd.SnapshotID          `json:"parents,omitempty"`
	Sources     []runtimecatalogcmd.SourceDescriptor    `json:"sources,omitempty"`
	Commands    []runtimecatalogcmd.CommandDescriptor   `json:"commands,omitempty"`
	Skills      []runtimecatalogcmd.SkillMetadata       `json:"skills,omitempty"`
	MCPServers  []runtimecatalogcmd.MCPServerDescriptor `json:"mcp_servers,omitempty"`
	Diagnostics []runtimecatalogcmd.Diagnostic          `json:"diagnostics,omitempty"`
}

func (r *Runtime) persistSnapshot(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) error {
	record := snapshotRecord{ID: snapshot.ID, Scope: snapshot.Scope, Parents: append([]runtimecatalogcmd.SnapshotID(nil), snapshot.Parents...), Diagnostics: append([]runtimecatalogcmd.Diagnostic(nil), snapshot.Diagnostics...)}
	for _, source := range snapshot.Sources {
		record.Sources = append(record.Sources, source)
	}
	for _, command := range snapshot.Commands {
		record.Commands = append(record.Commands, command)
	}
	for _, skill := range snapshot.Skills {
		record.Skills = append(record.Skills, skill)
	}
	for _, server := range snapshot.MCPServers {
		record.MCPServers = append(record.MCPServers, server)
	}
	sort.Slice(record.Sources, func(i, j int) bool { return record.Sources[i].ID.String() < record.Sources[j].ID.String() })
	sort.Slice(record.Commands, func(i, j int) bool { return record.Commands[i].ID.String() < record.Commands[j].ID.String() })
	sort.Slice(record.Skills, func(i, j int) bool { return record.Skills[i].ID.String() < record.Skills[j].ID.String() })
	sort.Slice(record.MCPServers, func(i, j int) bool { return record.MCPServers[i].ID.String() < record.MCPServers[j].ID.String() })
	if err := r.kv.SetJSON(ctx, snapshotKeyPrefix+string(snapshot.ID), record); err != nil {
		return fmt.Errorf("persist runtime catalog snapshot: %w", err)
	}
	return nil
}

func (r *Runtime) retainedSnapshot(ctx context.Context, id runtimecatalogcmd.SnapshotID) (runtimecatalogcmd.Snapshot, error) {
	if snapshot, err := r.store.Get(id); err == nil {
		return snapshot, nil
	}
	raw, found, err := r.kv.GetJSON(ctx, snapshotKeyPrefix+string(id))
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, err
	}
	if !found {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	data, err := json.Marshal(raw)
	if err != nil {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	var record snapshotRecord
	if err := json.Unmarshal(data, &record); err != nil || record.ID != id {
		return runtimecatalogcmd.Snapshot{}, runtimecatalogcmd.ErrSnapshotUnavailable
	}
	snapshot := runtimecatalogcmd.Snapshot{
		ID: record.ID, Scope: record.Scope, Parents: record.Parents, Diagnostics: record.Diagnostics,
		Sources:    make(map[runtimecatalogcmd.SourceID]runtimecatalogcmd.SourceDescriptor, len(record.Sources)),
		Commands:   make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor, len(record.Commands)),
		Skills:     make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata, len(record.Skills)),
		MCPServers: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor, len(record.MCPServers)),
	}
	for _, source := range record.Sources {
		snapshot.Sources[source.ID] = source
	}
	for _, command := range record.Commands {
		snapshot.Commands[command.ID] = command
	}
	for _, skill := range record.Skills {
		snapshot.Skills[skill.ID] = skill
	}
	for _, server := range record.MCPServers {
		snapshot.MCPServers[server.ID] = server
	}
	return r.store.Publish(snapshot)
}

func (r *Runtime) loadSkillSources(ctx context.Context, root string, id runtimecatalogcmd.SourceID) ([]runtimecatalogcmd.Source, error) {
	info, err := os.Stat(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect skill source: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("skill source is not a directory")
	}
	source, err := r.loader.LoadSkillSource(root, id)
	if err != nil {
		return nil, fmt.Errorf("load skill source: %w", err)
	}
	if err := r.archive.Retain(ctx, root, source.Descriptor); err != nil {
		return nil, fmt.Errorf("retain skill source: %w", err)
	}
	return []runtimecatalogcmd.Source{source}, nil
}

func (r *Runtime) retainPlugin(ctx context.Context, descriptor runtimecatalogcmd.SourceDescriptor) error {
	revision, found, err := r.plugins.GetPluginRevision(ctx, descriptor.ID.Name, string(descriptor.Revision))
	if err != nil {
		return err
	}
	if !found {
		return runtimecatalogcmd.ErrRevisionUnavailable
	}
	root, err := containedStatePath(r.stateDir, revision.RelativeRoot)
	if err != nil {
		return runtimecatalogcmd.ErrRevisionUnavailable
	}
	if err := r.archive.Retain(ctx, root, descriptor); err != nil {
		return fmt.Errorf("retain plugin revision: %w", err)
	}
	return nil
}

func builtinSource(advertisements []commandcmd.Advertisement) runtimecatalogcmd.Source {
	revision := hashValue(builtinRevisionSeed)
	id := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindBuiltin, Name: "balda"}
	names := make(map[string]struct{})
	for _, advertisement := range advertisements {
		for _, name := range advertisement.Names {
			name = strings.ToLower(strings.TrimSpace(name))
			if name != "" {
				names[name] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	source := runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: id, Revision: revision}}
	for _, name := range ordered {
		source.Commands = append(source.Commands, runtimecatalogcmd.CommandDescriptor{ID: runtimecatalogcmd.ContributionID{Source: id, Kind: runtimecatalogcmd.ContributionKindCommand, Name: name}, Revision: revision, Name: name})
	}
	return source
}

func configuredMCPSources(configs map[string]agentconfig.MCPServerConfig) []runtimecatalogcmd.Source {
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	sources := make([]runtimecatalogcmd.Source, 0, len(names))
	for _, name := range names {
		config := configs[name]
		id := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: name}
		revision := configuredMCPRevision(config)
		transport := string(config.Type)
		if transport == "http" {
			transport = "streamable-http"
		}
		sources = append(sources, runtimecatalogcmd.Source{
			Descriptor: runtimecatalogcmd.SourceDescriptor{ID: id, Revision: revision},
			MCPServers: []runtimecatalogcmd.MCPServerDescriptor{{ID: runtimecatalogcmd.ContributionID{Source: id, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: name}, Revision: revision, Name: name, Transport: transport}},
		})
	}
	return sources
}

func configuredMCPResolver(configs map[string]agentconfig.MCPServerConfig) *mcpruntime.StaticResolver {
	entries := make(map[runtimecatalogcmd.ContributionID]mcpruntime.StaticLaunchEntry, len(configs))
	for name, config := range configs {
		source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: name}
		id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: name}
		transport := string(config.Type)
		if transport == "http" {
			transport = "streamable-http"
		}
		command := ""
		args := append([]string(nil), config.Args...)
		if len(config.Cmd) > 0 {
			command = config.Cmd[0]
			args = append(append([]string(nil), config.Cmd[1:]...), args...)
		}
		entries[id] = mcpruntime.StaticLaunchEntry{Revision: configuredMCPRevision(config), Config: mcpruntime.LaunchConfig{
			Transport: transport, Command: command, Args: args, Env: config.Env, WorkingDir: config.WorkingDir, URL: config.URL, Headers: config.Headers,
		}}
	}
	return mcpruntime.NewStaticResolver(entries)
}

func configuredMCPRevision(config agentconfig.MCPServerConfig) runtimecatalogcmd.RevisionID {
	envKeys := sortedKeys(config.Env)
	headerKeys := sortedKeys(config.Headers)
	value := struct {
		Type       agentconfig.MCPServerType `json:"type"`
		Cmd        []string                  `json:"cmd,omitempty"`
		Args       []string                  `json:"args,omitempty"`
		WorkingDir string                    `json:"working_dir,omitempty"`
		URL        string                    `json:"url,omitempty"`
		EnvKeys    []string                  `json:"env_keys,omitempty"`
		HeaderKeys []string                  `json:"header_keys,omitempty"`
	}{config.Type, config.Cmd, config.Args, config.WorkingDir, config.URL, envKeys, headerKeys}
	data, _ := json.Marshal(value)
	return hashValue(string(data))
}

func hashValue(value string) runtimecatalogcmd.RevisionID {
	digest := sha256.Sum256([]byte(value))
	return runtimecatalogcmd.RevisionID(hex.EncodeToString(digest[:]))
}

func sortedKeys(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func workspaceScopeName(workspace string) string { return string(hashValue(filepath.Clean(workspace))) }

func containedStatePath(stateDir, relative string) (string, error) {
	path := filepath.FromSlash(strings.TrimSpace(relative))
	if path == "" || filepath.IsAbs(path) {
		return "", errors.New("relative state path is required")
	}
	clean := filepath.Clean(path)
	if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", errors.New("state path escapes root")
	}
	root, err := filepath.EvalSymlinks(stateDir)
	if err != nil {
		return "", err
	}
	target, err := filepath.EvalSymlinks(filepath.Join(root, clean))
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, target)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("state path escapes root")
	}
	return target, nil
}
