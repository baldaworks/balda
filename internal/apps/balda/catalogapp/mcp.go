package catalogapp

import (
	"context"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// MCPHealth returns only observed readiness for the exact selected revision.
func (r *Runtime) MCPHealth(ctx context.Context, c mcpcmd.Connection) (mcpcmd.Status, int, error) {
	if err := ctx.Err(); err != nil {
		return mcpcmd.StatusUnavailable, 0, err
	}
	key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: c.ID}, Revision: runtimecatalogcmd.RevisionID(c.CurrentRevisionID), Name: c.PublicID}
	if c.Source == mcpcmd.SourceConfig {
		key.Source = runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: c.PublicID}
		for _, source := range r.configuredMCP {
			if source.Descriptor.ID == key.Source {
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
		if c.Source != mcpcmd.SourceManaged || c.PublishedVersion == c.Version {
			continue
		}
		source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: c.ID}
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
