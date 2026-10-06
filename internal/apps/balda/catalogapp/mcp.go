package catalogapp

import (
	"context"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// Credential observations supplement discovery without changing its lifetime.
type mcpCredentialHealth interface {
	CredentialsUnavailable(id string) bool
	RetryCredentials(ctx context.Context, id string) error
}

// MCPHealth returns only observed readiness for the exact selected revision.
func (r *Runtime) MCPHealth(ctx context.Context, c mcpcmd.Connection) (mcpcmd.Status, int, error) {
	if err := ctx.Err(); err != nil {
		return mcpcmd.StatusUnavailable, 0, err
	}
	currentStdio := false
	if c.Source == mcpcmd.SourceConfig {
		for _, source := range r.configuredMCP {
			if source.Descriptor.ID.Name == c.PublicID && source.MCPServers[0].Transport == transportStdio {
				currentStdio = true
				break
			}
		}
	}
	if c.CurrentRevisionID != "" && !currentStdio {
		revision, found, err := r.managedMCP.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
		if err != nil || !found {
			return mcpcmd.StatusUnavailable, 0, mcpcmd.ErrUnavailable
		}
		if c.Source == mcpcmd.SourceConfig {
			matches := false
			for _, source := range r.configuredMCP {
				if source.Descriptor.ID.Name == c.PublicID && string(source.Descriptor.Revision) == revision.Definition.ConfigRevision {
					matches = true
					break
				}
			}
			if !matches {
				return mcpcmd.StatusAuthRequired, 0, nil
			}
		}
		if revision.Definition.OAuth {
			binding := revision.Definition.AuthBinding
			if binding == nil || binding.ConnectionID != c.ID || binding.Resource != revision.Definition.URL {
				return mcpcmd.StatusAuthRequired, 0, nil
			}
			grant, found, err := r.managedMCP.GetMCPGrant(ctx, *binding)
			if err != nil {
				return mcpcmd.StatusUnavailable, 0, mcpcmd.ErrUnavailable
			}
			if !found || grant.Status == mcpcmd.GrantAuthRequired {
				return mcpcmd.StatusAuthRequired, 0, nil
			}
			if grant.Status == mcpcmd.GrantDisconnected {
				return mcpcmd.StatusDisconnected, 0, nil
			}
		}
	}
	key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: c.ID}, Revision: runtimecatalogcmd.RevisionID(c.CurrentRevisionID), Name: c.PublicID}
	if c.Source == mcpcmd.SourceConfig {
		key.Source = runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: c.PublicID}
		for _, source := range r.configuredMCP {
			if (c.CurrentRevisionID == "" || currentStdio) && source.Descriptor.ID == key.Source {
				key.Revision = source.Descriptor.Revision
				break
			}
		}
	}
	for _, health := range r.mcp.Health() {
		if health.Key != key {
			continue
		}
		switch health.State {
		case mcpruntime.HealthReady:
			if r.mcpCredentials != nil && r.mcpCredentials.CredentialsUnavailable(mcpfx.RegistryID(key)) {
				return mcpcmd.StatusUnavailable, 0, nil
			}
			return mcpcmd.StatusReady, health.ToolCount, nil
		case mcpruntime.HealthFailed, mcpruntime.HealthDegraded, mcpruntime.HealthStopping:
			return mcpcmd.StatusUnavailable, 0, nil
		default:
			return mcpcmd.StatusPending, 0, nil
		}
	}
	return mcpcmd.StatusPending, 0, nil
}

// LockCatalogMutation holds the outer commit/publication gate shared by plugin
// and MCP mutations. Callers release it after marking their durable completion.
// It is distinct from Runtime.mu, which protects candidate compilation.
func (r *Runtime) LockCatalogMutation(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	r.mutation.Lock()
	if err := ctx.Err(); err != nil {
		r.mutation.Unlock()
		return nil, err
	}
	return r.mutation.Unlock, nil
}

// PublishMCP commits a managed mutation and reconstructs the complete catalog
// from current durable sources. A failed publication leaves its marker pending.
func (r *Runtime) PublishMCP(ctx context.Context, commit func() error) error {
	if commit == nil {
		return mcpcmd.ErrInvalid
	}
	release, err := r.LockCatalogMutation(ctx)
	if err != nil {
		return err
	}
	defer release()
	if err := commit(); err != nil {
		return err
	}
	sources, err := r.currentPluginSources(ctx)
	if err != nil {
		return err
	}
	snapshot, err := r.PreparePluginCandidate(ctx, sources)
	if err != nil {
		return err
	}
	return r.PublishCandidate(ctx, snapshot)
}

func (r *Runtime) currentPluginSources(ctx context.Context) ([]runtimecatalogcmd.Source, error) {
	installs, err := r.plugins.ListPluginInstalls(ctx)
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	var sources []runtimecatalogcmd.Source
	for _, install := range installs {
		if !install.Enabled {
			continue
		}
		revision, found, err := r.plugins.GetPluginRevision(ctx, install.PluginID, install.ActiveRevisionID)
		if err != nil || !found {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		root, err := containedStatePath(r.stateDir, filepath.FromSlash(revision.RelativeRoot))
		if err != nil {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		source, err := r.loader.LoadPlugin(root)
		if err != nil || source.Descriptor.ID.Name != install.PluginID || string(source.Descriptor.Revision) != revision.RevisionID {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		sources = append(sources, source)
	}
	return sources, nil
}

func managedMCPDescriptor(c mcpcmd.Connection, revision mcpcmd.Revision) runtimecatalogcmd.MCPServerDescriptor {
	transport := string(revision.Definition.Transport)
	if revision.Definition.Transport == mcpcmd.TransportHTTP {
		transport = transportStreamableHTTP
	}
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: c.ID}
	return runtimecatalogcmd.MCPServerDescriptor{ID: runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: c.PublicID}, Revision: runtimecatalogcmd.RevisionID(revision.ID), Name: c.PublicID, Transport: transport, ConfigRef: c.ID}
}

// currentConfiguredMCPSources attaches file-owned entries to exact protected
// OAuth captures. A changed file cannot fall back to anonymous static launch.
func (r *Runtime) currentConfiguredMCPSources(ctx context.Context) ([]runtimecatalogcmd.Source, error) {
	connections, err := r.managedMCP.ListMCPConnections(ctx)
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	byName := make(map[string]mcpcmd.Connection)
	for _, c := range connections {
		if c.Source == mcpcmd.SourceConfig {
			byName[c.PublicID] = c
		}
	}
	sources := make([]runtimecatalogcmd.Source, len(r.configuredMCP))
	for i, base := range r.configuredMCP {
		sources[i] = base
		sources[i].MCPServers = append([]runtimecatalogcmd.MCPServerDescriptor(nil), base.MCPServers...)
		c, ok := byName[base.Descriptor.ID.Name]
		if !ok {
			continue
		}
		revision, found, err := r.managedMCP.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
		if err != nil || !found || !revision.Definition.OAuth || c.Deleted {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		descriptor := &sources[i].MCPServers[0]
		if descriptor.Transport == transportStdio {
			// The current file owns this transport. The old OAuth capture is
			// retained solely for exact historical remote pins.
			continue
		}
		descriptor.ConfigRef = c.ID
		if revision.Definition.ConfigRevision == string(base.Descriptor.Revision) {
			descriptor.Revision = runtimecatalogcmd.RevisionID(revision.ID)
			sources[i].Descriptor.Revision = descriptor.Revision
		}
		// The current-file adapter recognizes a validated mismatch; retained
		// resolution still refuses arbitrary missing capture IDs.
	}
	return sources, nil
}

func (r *Runtime) managedMCPSources(ctx context.Context) ([]runtimecatalogcmd.Source, error) {
	connections, err := r.managedMCP.ListMCPConnections(ctx)
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	configured := make(map[string]bool, len(r.configuredMCP))
	for _, source := range r.configuredMCP {
		configured[source.Descriptor.ID.Name] = true
	}
	var sources []runtimecatalogcmd.Source
	for _, c := range connections {
		if c.Source != mcpcmd.SourceManaged {
			continue
		}
		// Tombstones retain public identity; a config collision never shadows
		// either connection or transfers old worker grants to a new owner.
		if configured[c.PublicID] {
			return nil, mcpcmd.ErrConflict
		}
		if !c.Enabled || c.Deleted {
			continue
		}
		revision, found, err := r.managedMCP.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
		if err != nil || !found {
			return nil, runtimecatalogcmd.ErrRevisionUnavailable
		}
		descriptor := managedMCPDescriptor(c, revision)
		sources = append(sources, runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: descriptor.ID.Source, Revision: descriptor.Revision}, MCPServers: []runtimecatalogcmd.MCPServerDescriptor{descriptor}})
	}
	return sources, nil
}

func (r *Runtime) markMCPPublished(ctx context.Context, snapshot runtimecatalogcmd.Snapshot) error {
	connections, err := r.managedMCP.ListMCPConnections(ctx)
	if err != nil {
		return mcpcmd.ErrUnavailable
	}
	for _, c := range connections {
		if c.PublishedVersion == c.Version {
			continue
		}
		source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: c.ID}
		if c.Source == mcpcmd.SourceConfig {
			source = runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: c.PublicID}
			id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: c.PublicID}
			descriptor, present := snapshot.MCPServers[id]
			// An edited/removed file leaves this auth capture pending until
			// an administrator captures the new file-owned definition.
			if !present || descriptor.ConfigRef != c.ID || string(descriptor.Revision) != c.CurrentRevisionID {
				continue
			}
		}
		id := runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: c.PublicID}
		descriptor, present := snapshot.MCPServers[id]
		if c.Enabled && !c.Deleted {
			if !present || descriptor.Revision != runtimecatalogcmd.RevisionID(c.CurrentRevisionID) {
				return mcpcmd.ErrConflict
			}
		} else if present {
			return mcpcmd.ErrConflict
		}
		if err := r.managedMCP.MarkMCPPublished(ctx, c.ID, c.Version); err != nil {
			return mcpcmd.ErrUnavailable
		}
	}
	return nil
}
