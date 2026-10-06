package catalogapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// CurrentMCPRecovery reads trusted repair metadata for the current inventory
// identity. Historical failures and public health strings are not evidence.
func (r *Runtime) CurrentMCPRecovery(ctx context.Context, item mcpcmd.Item) (mcpcmd.RecoveryReason, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c := item.Connection
	if !c.Enabled || c.Deleted || item.Status == mcpcmd.StatusReady || item.Status == mcpcmd.StatusConflict {
		return "", nil
	}
	if c.CurrentRevisionID != "" {
		current, found, err := r.managedMCP.GetMCPConnection(ctx, c.ID)
		if err != nil {
			return "", mcpcmd.ErrUnavailable
		}
		if !found || current != c {
			return "", nil
		}
	}
	snapshot, err := r.store.Application()
	if err != nil {
		return "", nil
	}
	kind, name := runtimecatalogcmd.SourceKindManagedMCP, c.ID
	if c.Source == mcpcmd.SourceConfig {
		kind, name = runtimecatalogcmd.SourceKindConfiguredMCP, c.PublicID
	} else if c.Source != mcpcmd.SourceManaged {
		return "", nil
	}
	id := runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: kind, Name: name}, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: c.PublicID}
	descriptor, found := snapshot.MCPServers[id]
	if !found || descriptor.Transport == transportStdio {
		return "", nil
	}
	if c.CurrentRevisionID == "" {
		if c.Source != mcpcmd.SourceConfig || descriptor.ConfigRef != "" {
			return "", nil
		}
	} else if descriptor.ConfigRef != c.ID || kind == runtimecatalogcmd.SourceKindManagedMCP && string(descriptor.Revision) != c.CurrentRevisionID {
		return "", nil
	}
	key := mcpruntime.InstanceKey{Source: descriptor.ID.Source, Revision: descriptor.Revision, Name: descriptor.Name}
	reason, found := r.mcp.Failure(key)
	if !found || !r.currentAuthorizationBlocker(ctx, descriptor, reason) {
		return "", nil
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch reason {
	case mcpruntime.FailureAuthorizationChallenge:
		return mcpcmd.RecoveryFirstAuthorization, nil
	case mcpruntime.FailureCaptureRequired:
		return mcpcmd.RecoveryCaptureRequired, nil
	case mcpruntime.FailureAuthorizationRequired:
		return mcpcmd.RecoveryAuthorizationRequired, nil
	default:
		return "", nil
	}
}
