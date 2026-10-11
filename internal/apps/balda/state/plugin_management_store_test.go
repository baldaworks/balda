package state

import (
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
)

func TestPluginManagementRequiresBrowserAuthority(t *testing.T) {
	s := &sqlPluginManagementStore{}
	if err := s.CommitPlugin(t.Context(), PluginCommit{}); !errors.Is(err, plugincmd.ErrInvalid) {
		t.Fatalf("missing authority: %v", err)
	}
	if err := s.CommitMarketplace(t.Context(), MarketplaceCommit{}); !errors.Is(err, plugincmd.ErrInvalid) {
		t.Fatalf("missing authority: %v", err)
	}
}
