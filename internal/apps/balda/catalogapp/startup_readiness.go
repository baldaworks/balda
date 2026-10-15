package catalogapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const transportStdio = "stdio"

// currentAuthorizationBlocker validates exact local recovery metadata. It does
// not admit partial provider shells or perform remote availability discovery.
func (r *Runtime) currentAuthorizationBlocker(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor, reason mcpruntime.FailureReason) bool {
	if descriptor.Transport == transportStdio {
		return false
	}
	switch reason {
	case mcpruntime.FailureAuthorizationChallenge:
		return descriptor.ConfigRef == "" && r.currentConfiguredDescriptor(descriptor)
	case mcpruntime.FailureCaptureRequired:
		if !r.currentConfiguredDescriptor(descriptor) {
			return false
		}
		capture, err := r.configuredCapture(ctx, descriptor)
		return err == nil && capture.Definition.ConfigRevision != string(descriptor.Revision)
	case mcpruntime.FailureAuthorizationRequired:
		kind := descriptor.ID.Source.Kind
		if kind != runtimecatalogcmd.SourceKindManagedMCP && kind != runtimecatalogcmd.SourceKindConfiguredMCP || descriptor.ConfigRef == "" || r.credentials == nil {
			return false
		}
		connection, found, err := r.managedMCP.GetMCPConnection(ctx, descriptor.ConfigRef)
		if err != nil || !found || !connection.Enabled || connection.Deleted || connection.PublicID != descriptor.Name || connection.CurrentRevisionID != string(descriptor.Revision) {
			return false
		}
		if kind == runtimecatalogcmd.SourceKindManagedMCP && (connection.Source != mcpcmd.SourceManaged || connection.ID != descriptor.ID.Source.Name) || kind == runtimecatalogcmd.SourceKindConfiguredMCP && connection.Source != mcpcmd.SourceConfig {
			return false
		}
		capture, found, err := r.managedMCP.GetMCPRevision(ctx, connection.ID, connection.CurrentRevisionID)
		return err == nil && found && capture.ConnectionID == connection.ID && capture.ID == connection.CurrentRevisionID && capture.Definition.OAuth && r.credentials.ValidateTransportRevision(capture) == nil
	default:
		return false
	}
}

func (r *Runtime) currentConfiguredDescriptor(descriptor runtimecatalogcmd.MCPServerDescriptor) bool {
	for _, source := range r.configuredMCP {
		current := source.MCPServers[0]
		if current.ID == descriptor.ID && current.Revision == descriptor.Revision && current.Transport == descriptor.Transport && current.Name == descriptor.Name {
			return true
		}
	}
	return false
}

func (r *Runtime) configuredCapture(ctx context.Context, descriptor runtimecatalogcmd.MCPServerDescriptor) (mcpcmd.Revision, error) {
	connection, found, err := r.managedMCP.GetMCPConnection(ctx, descriptor.ConfigRef)
	if err != nil || !found || connection.Source != mcpcmd.SourceConfig || connection.PublicID != descriptor.Name || connection.Deleted {
		return mcpcmd.Revision{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	capture, found, err := r.managedMCP.GetMCPRevision(ctx, connection.ID, connection.CurrentRevisionID)
	if err != nil || !found || capture.ConnectionID != connection.ID || capture.ID != connection.CurrentRevisionID || !capture.Definition.OAuth || capture.Definition.ConfigRevision == "" {
		return mcpcmd.Revision{}, runtimecatalogcmd.ErrRevisionUnavailable
	}
	if r.credentials == nil {
		return mcpcmd.Revision{}, mcpcmd.ErrCredentials
	}
	if err := r.credentials.ValidateTransportRevision(capture); err != nil {
		return mcpcmd.Revision{}, err
	}
	return capture, nil
}
