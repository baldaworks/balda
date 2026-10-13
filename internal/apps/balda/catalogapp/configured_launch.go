package catalogapp

import (
	"context"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// This composition adapter knows the immutable current file, unlike the
// retained resolver. A known uncaptured file digest is never a historical ID.
type configuredLaunchResolver struct {
	catalog  *Runtime
	fallback mcpruntime.LaunchResolver
	current  mcpruntime.LaunchResolver
	bridge   *mcpbridge.Bridge
}

func (r configuredLaunchResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if descriptor.ConfigRef != "" && descriptor.Transport != transportStdio && r.catalog.currentConfiguredDescriptor(descriptor) {
		capture, err := r.catalog.configuredCapture(ctx, descriptor)
		if err != nil {
			return mcpruntime.LaunchConfig{}, err
		}
		if capture.Definition.ConfigRevision != string(descriptor.Revision) {
			config, err := r.current.ResolveLaunch(ctx, descriptor)
			if err != nil {
				return mcpruntime.LaunchConfig{}, err
			}
			key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
			return mcpfx.AuthorizationRequiredLaunch(r.bridge, mcpfx.RegistryID(key), config)
		}
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return r.fallback.ResolveLaunch(ctx, descriptor)
}
