package mcpfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type managedRevisionStore interface {
	GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error)
	GetMCPRevision(ctx context.Context, connectionID, revisionID string) (mcpcmd.Revision, bool, error)
}

// ManagedResolver resolves exact retained definitions independently of current
// enabled/deleted selection. No protected value enters a catalog descriptor.
type ManagedResolver struct {
	Store  managedRevisionStore
	Values managedValueResolver
}

// ResolveLaunch resolves one exact managed revision at the trusted boundary.
func (r *ManagedResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if r == nil || r.Store == nil || descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindManagedMCP || descriptor.ID.Kind != runtimecatalogcmd.ContributionKindMCPServer || descriptor.ConfigRef != descriptor.ID.Source.Name || descriptor.Name != descriptor.ID.Name || descriptor.Revision == "" {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	c, found, err := r.Store.GetMCPConnection(ctx, descriptor.ID.Source.Name)
	if err != nil || !found || c.Source != mcpcmd.SourceManaged || c.PublicID != descriptor.Name {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	revision, found, err := r.Store.GetMCPRevision(ctx, c.ID, string(descriptor.Revision))
	if err != nil || !found || revision.ConnectionID != c.ID || revision.ID != string(descriptor.Revision) {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	config, err := ResolveManagedLaunch(revision, r.Values)
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	if config.Transport != descriptor.Transport {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return config, nil
}
