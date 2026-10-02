package mcpfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// GrantStore translates the feature's fenced grant writes to shared SQL state.
type GrantStore struct{ state.MCPStore }

// NewGrantStore binds worker authorization to the existing application database.
func NewGrantStore(store state.MCPStore) *GrantStore { return &GrantStore{MCPStore: store} }

// SaveGrant commits credentials, authority and safe audit atomically.
func (s *GrantStore) SaveGrant(ctx context.Context, m mcpmanage.GrantMutation) error {
	return s.SaveMCPGrant(ctx, state.MCPGrantMutation{Grant: m.Grant, Operation: m.Operation, ExpectedGeneration: m.ExpectedGeneration, Authority: m.Authority, Audit: m.Audit})
}
