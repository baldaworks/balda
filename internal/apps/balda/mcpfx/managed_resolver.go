package mcpfx

import (
	"context"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type managedRevisionStore interface {
	GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error)
	GetMCPRevision(ctx context.Context, connectionID, revisionID string) (mcpcmd.Revision, bool, error)
}

// ManagedResolver resolves exact retained definitions independently of current
// enabled/deleted selection. Configured OAuth side records use the same retained boundary.
// No protected value enters a catalog descriptor.
type ManagedResolver struct {
	Store  managedRevisionStore
	Values managedValueResolver
	Bridge *mcpbridge.Bridge
}

// ResolveLaunch resolves one exact managed revision at the trusted boundary.
func (r *ManagedResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if r == nil || r.Store == nil || descriptor.ID.Kind != runtimecatalogcmd.ContributionKindMCPServer || descriptor.ConfigRef == "" || descriptor.Name != descriptor.ID.Name || descriptor.Revision == "" {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	source := mcpcmd.SourceManaged
	switch descriptor.ID.Source.Kind {
	case runtimecatalogcmd.SourceKindManagedMCP:
		if descriptor.ConfigRef != descriptor.ID.Source.Name {
			return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
		}
	case runtimecatalogcmd.SourceKindConfiguredMCP:
		source = mcpcmd.SourceConfig
		if descriptor.ID.Source.Name != descriptor.Name {
			return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
		}
	default:
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	c, found, err := r.Store.GetMCPConnection(ctx, descriptor.ConfigRef)
	if err != nil || !found || c.Source != source || c.PublicID != descriptor.Name {
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
	binding := revision.Definition.AuthBinding
	if revision.Definition.OAuth {
		if binding == nil || binding.ConnectionID != c.ID || binding.Resource != config.URL || binding.Issuer == "" || binding.ClientID == "" {
			return mcpruntime.LaunchConfig{}, errors.Join(mcpcmd.ErrAuthRequired, &mcpruntime.LaunchError{Reason: mcpruntime.FailureAuthorizationRequired})
		}
	} else if binding != nil {
		return mcpruntime.LaunchConfig{}, mcpcmd.ErrInvalid
	}
	key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
	return BridgeLaunch(r.Bridge, RegistryID(key), config, binding, revision.Definition.Scopes)
}

// ConfiguredResolver keeps static file launches separate from exact protected
// side records. A retained descriptor never falls back to the current file.
type ConfiguredResolver struct {
	Static   mcpruntime.LaunchResolver
	Retained mcpruntime.LaunchResolver
}

func (r ConfiguredResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if descriptor.ConfigRef != "" {
		if r.Retained == nil {
			return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
		}
		return r.Retained.ResolveLaunch(ctx, descriptor)
	}
	if r.Static == nil {
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return r.Static.ResolveLaunch(ctx, descriptor)
}
