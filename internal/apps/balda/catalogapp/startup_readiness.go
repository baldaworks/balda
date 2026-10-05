package catalogapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

const transportStdio = "stdio"

// MCPAuthorizationPending reports whether every selected startup blocker has
// trusted current authorization evidence. Execution still fails acquisition.
func (r *Runtime) MCPAuthorizationPending(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil {
		return false
	}
	snapshot, snapshotErr := r.store.Application()
	if snapshotErr != nil {
		return false
	}
	return r.authorizationBlockers(ctx, snapshot, err)
}

func (r *Runtime) authorizationBlockers(ctx context.Context, snapshot runtimecatalogcmd.Snapshot, err error) bool {
	switch failure := err.(type) {
	case *mcpruntime.AttachmentError:
		for _, descriptor := range snapshot.MCPServers {
			key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
			if key == failure.Key {
				return r.currentAuthorizationBlocker(ctx, descriptor, failure.Reason)
			}
		}
		return false
	case interface{ Unwrap() []error }:
		children := failure.Unwrap()
		if len(children) == 0 {
			return false
		}
		for _, child := range children {
			if !r.authorizationBlockers(ctx, snapshot, child) {
				return false
			}
		}
		return true
	case interface{ Unwrap() error }:
		return r.authorizationBlockers(ctx, snapshot, failure.Unwrap())
	default:
		return false
	}
}

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
