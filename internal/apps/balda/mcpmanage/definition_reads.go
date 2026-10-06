package mcpmanage

import (
	"context"
	"slices"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// ProviderIDs reads the configured provider choices from their existing owner.
func (s *Definitions) ProviderIDs(ctx context.Context) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	ids, err := s.configured.ProviderIDs(ctx)
	if err != nil {
		return nil, safeOperationError(err)
	}
	return slices.Clone(ids), nil
}

// WorkerAuthorization reports only the exact current definition's safe grant
// status. A different file capture or stdio declaration never inherits a grant.
func (s *Definitions) WorkerAuthorization(ctx context.Context, item mcpcmd.Item) (mcpcmd.GrantStatus, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c := item.Connection
	if c.Source == mcpcmd.SourceConfig {
		configured, err := s.configured.MCPDefinitions(ctx)
		if err != nil {
			return "", safeOperationError(err)
		}
		matches := false
		for _, current := range configured {
			if current.Connection.PublicID == c.PublicID && current.Definition.Transport == item.Definition.Transport && current.Definition.ConfigRevision == item.Definition.ConfigRevision && current.Definition.Transport != mcpcmd.TransportStdio {
				matches = true
				break
			}
		}
		if !matches || c.CurrentRevisionID == "" {
			return "", nil
		}
	} else if c.Source != mcpcmd.SourceManaged {
		return "", mcpcmd.ErrInvalid
	}
	current, found, err := s.store.GetMCPConnection(ctx, c.ID)
	if err != nil {
		return "", safeOperationError(err)
	}
	if !found || current != c {
		return "", mcpcmd.ErrConflict
	}
	r, found, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
	if err != nil {
		return "", safeOperationError(err)
	}
	if !found || r.ConnectionID != c.ID || r.ID != c.CurrentRevisionID {
		return "", mcpcmd.ErrUnavailable
	}
	if c.Source == mcpcmd.SourceConfig && r.Definition.ConfigRevision != item.Definition.ConfigRevision {
		return "", nil
	}
	if !r.Definition.OAuth || r.Definition.Transport == mcpcmd.TransportStdio {
		return "", nil
	}
	if err := s.credentials.ValidateTransportRevision(r); err != nil {
		return "", safeOperationError(err)
	}
	if r.Definition.AuthBinding == nil {
		return mcpcmd.GrantAuthRequired, nil
	}
	grant, found, err := s.store.GetMCPGrant(ctx, *r.Definition.AuthBinding)
	if err != nil {
		return "", safeOperationError(err)
	}
	if !found {
		return mcpcmd.GrantAuthRequired, nil
	}
	if grant.Binding != *r.Definition.AuthBinding {
		return "", mcpcmd.ErrCredentials
	}
	if _, err := s.credentials.OpenGrant(grant); err != nil {
		return "", safeOperationError(err)
	}
	return grant.Status, nil
}
