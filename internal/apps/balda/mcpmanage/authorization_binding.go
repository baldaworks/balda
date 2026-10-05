package mcpmanage

import (
	"context"
	"crypto/rand"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// BindAuthorization selects only a durable protocol-discovered worker grant.
// A different identity creates a revision; renewing the same grant does not.
func (s *Definitions) BindAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error) {
	request.Authority.At = time.Now().UTC()
	if err := s.store.CheckMCPAuthority(ctx, request.Authority); err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	c, found, err := s.store.GetMCPConnection(ctx, request.ConnectionID)
	if err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	if !found {
		return mcpcmd.Item{}, mcpcmd.ErrNotFound
	}
	if c.Deleted || request.ExpectedRevisionID == "" || c.CurrentRevisionID != request.ExpectedRevisionID {
		return mcpcmd.Item{}, mcpcmd.ErrConflict
	}
	previous, found, err := s.store.GetMCPRevision(ctx, c.ID, c.CurrentRevisionID)
	if err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	if !found {
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}
	binding := request.Binding
	definition := previous.Definition
	if !definition.OAuth || definition.Transport == mcpcmd.TransportStdio || binding.ConnectionID != c.ID || binding.Resource != definition.URL || !validRemoteURL(binding.Issuer) || binding.ClientID == "" || len(binding.ClientID) > 1024 {
		return mcpcmd.Item{}, mcpcmd.ErrInvalid
	}
	grant, found, err := s.store.GetMCPGrant(ctx, binding)
	if err != nil {
		return mcpcmd.Item{}, safeOperationError(err)
	}
	if !found || grant.Binding != binding || !containsScopes(grant.Scopes, definition.Scopes) {
		return mcpcmd.Item{}, mcpcmd.ErrAuthRequired
	}
	if grant.Status == mcpcmd.GrantDisconnected {
		return mcpcmd.Item{}, mcpcmd.ErrDisconnected
	}
	if grant.Status != mcpcmd.GrantAuthorized && grant.Status != mcpcmd.GrantAuthRequired {
		return mcpcmd.Item{}, mcpcmd.ErrUnavailable
	}
	if definition.AuthBinding != nil && *definition.AuthBinding == binding {
		// The grant owner has released its lock before binding. Retry only
		// affected failed attachments; no definition/snapshot write is needed.
		retryErr := s.catalog.RetryMCPAuthorization(ctx, binding)
		item, err := s.item(ctx, c)
		if err != nil {
			return item, err
		}
		return item, safeOperationError(retryErr)
	}
	definition.AuthBinding = &binding
	c.CurrentRevisionID, c.UpdatedAt = rand.Text(), request.Authority.At
	next, err := s.credentials.PrepareRevision(&previous, mcpcmd.Revision{ConnectionID: c.ID, ID: c.CurrentRevisionID, Definition: definition, CreatedAt: c.UpdatedAt}, mcpcmd.ValueEdits{})
	if err != nil {
		return mcpcmd.Item{}, err
	}
	return s.save(ctx, c, &next, c.Version, request.Authority)
}
