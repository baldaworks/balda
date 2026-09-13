package mcpruntime

import (
	"context"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// StaticLaunchEntry binds one configured MCP descriptor to host-owned config.
type StaticLaunchEntry struct {
	Revision runtimecatalogcmd.RevisionID
	Config   LaunchConfig
}

// StaticResolver resolves configured host MCP servers through the same launch port.
type StaticResolver struct {
	entries map[runtimecatalogcmd.ContributionID]StaticLaunchEntry
}

// NewStaticResolver creates an immutable configured-server resolver.
func NewStaticResolver(entries map[runtimecatalogcmd.ContributionID]StaticLaunchEntry) *StaticResolver {
	cloned := make(map[runtimecatalogcmd.ContributionID]StaticLaunchEntry, len(entries))
	for id, entry := range entries {
		entry.Config = cloneLaunchConfig(entry.Config)
		cloned[id] = entry
	}
	return &StaticResolver{entries: cloned}
}

// ResolveLaunch resolves one exact configured revision.
func (r *StaticResolver) ResolveLaunch(_ context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	if r == nil || descriptor.ID.Source.Kind != runtimecatalogcmd.SourceKindConfiguredMCP {
		return LaunchConfig{}, errors.New("configured MCP descriptor is unsupported")
	}
	entry, ok := r.entries[descriptor.ID]
	if !ok || entry.Revision != descriptor.Revision {
		return LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return cloneLaunchConfig(entry.Config), nil
}

// RoutedResolver keeps configured and plugin launch resolution explicit.
type RoutedResolver struct {
	Configured LaunchResolver
	Plugin     LaunchResolver
}

// ResolveLaunch routes by structured source kind without fallback rebinding.
func (r RoutedResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (LaunchConfig, error) {
	switch descriptor.ID.Source.Kind {
	case runtimecatalogcmd.SourceKindConfiguredMCP:
		if r.Configured == nil {
			return LaunchConfig{}, errors.New("configured MCP resolver is unavailable")
		}
		return r.Configured.ResolveLaunch(ctx, descriptor)
	case runtimecatalogcmd.SourceKindPlugin:
		if r.Plugin == nil {
			return LaunchConfig{}, errors.New("plugin MCP resolver is unavailable")
		}
		return r.Plugin.ResolveLaunch(ctx, descriptor)
	default:
		return LaunchConfig{}, errors.New("MCP source kind is unsupported")
	}
}
