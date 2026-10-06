package catalogapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// RetryMCPAuthorization retries only failed exact attachments sharing the
// installed worker identity. It never publishes or replaces a ready runner.
func (r *Runtime) RetryMCPAuthorization(ctx context.Context, binding mcpcmd.AuthBinding) error {
	unlock, err := r.LockCatalogMutation(ctx)
	if err != nil {
		return err
	}
	defer unlock()
	connection, found, err := r.managedMCP.GetMCPConnection(ctx, binding.ConnectionID)
	if err != nil || !found {
		return mcpcmd.ErrUnavailable
	}
	var keys []mcpruntime.InstanceKey
	for _, health := range r.mcp.Health() {
		if health.State != mcpruntime.HealthFailed {
			continue
		}
		kind := health.Key.Source.Kind
		switch {
		case kind == runtimecatalogcmd.SourceKindManagedMCP && connection.Source == mcpcmd.SourceManaged:
			if health.Key.Source.Name != connection.ID || health.Key.Name != connection.PublicID {
				continue
			}
		case kind == runtimecatalogcmd.SourceKindConfiguredMCP && connection.Source == mcpcmd.SourceConfig:
			if health.Key.Source.Name != connection.PublicID || health.Key.Name != connection.PublicID {
				continue
			}
			// A known current file digest (before capture or during recapture)
			// has no installed worker identity. It is not a stored revision ID.
			file := false
			for _, source := range r.configuredMCP {
				if source.Descriptor.ID == health.Key.Source && source.Descriptor.Revision == health.Key.Revision && source.MCPServers[0].Name == health.Key.Name {
					file = true
					break
				}
			}
			if file {
				continue
			}
		default:
			continue
		}
		revision, found, err := r.managedMCP.GetMCPRevision(ctx, connection.ID, string(health.Key.Revision))
		if err != nil || !found {
			return mcpcmd.ErrUnavailable
		}
		if revision.Definition.AuthBinding != nil && *revision.Definition.AuthBinding == binding {
			keys = append(keys, health.Key)
		}
	}
	return r.mcp.Retry(ctx, keys)
}
