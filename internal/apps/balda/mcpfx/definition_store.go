package mcpfx

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// DefinitionStore adapts the shared SQL capability to the feature's local port.
type DefinitionStore struct{ state.MCPStore }

// NewDefinitionStore uses the application's existing state provider.
func NewDefinitionStore(store state.MCPStore) *DefinitionStore {
	return &DefinitionStore{MCPStore: store}
}

// SaveDefinition preserves one atomic authority/definition/audit transaction.
func (s *DefinitionStore) SaveDefinition(ctx context.Context, m mcpmanage.Mutation) error {
	return s.SaveMCPConnection(ctx, state.MCPMutation{Connection: m.Connection, Revision: m.Revision, ExpectedVersion: m.ExpectedVersion, Authority: m.Authority, Audit: m.Audit})
}

var _ mcpmanage.DefinitionStore = (*DefinitionStore)(nil)
