package catalogapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// This composition adapter knows the immutable current file, unlike the
// retained resolver. A known uncaptured file digest is never a historical ID.
type configuredLaunchResolver struct {
	catalog  *Runtime
	fallback mcpruntime.LaunchResolver
}

func (r configuredLaunchResolver) ResolveLaunch(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpruntime.LaunchConfig, error) {
	if descriptor.ConfigRef != "" && descriptor.Transport != transportStdio && r.catalog.currentConfiguredDescriptor(descriptor) {
		capture, err := r.catalog.configuredCapture(ctx, descriptor)
		if err != nil {
			return mcpruntime.LaunchConfig{}, err
		}
		if capture.Definition.ConfigRevision != string(descriptor.Revision) {
			return mcpruntime.LaunchConfig{}, &mcpruntime.LaunchError{Reason: mcpruntime.FailureCaptureRequired}
		}
		return mcpruntime.LaunchConfig{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	return r.fallback.ResolveLaunch(ctx, descriptor)
}
