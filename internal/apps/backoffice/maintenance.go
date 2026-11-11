package backoffice

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// Maintenance owns the offline administrator operations retained in cmd/balda.
// It has no HTTP server or browser application lifecycle.
type Maintenance struct {
	provider  state.Provider
	bootstrap *BootstrapService
}

// OpenMaintenance opens Core state for one offline maintenance command.
func OpenMaintenance(ctx context.Context, database state.DatabaseConfig) (*Maintenance, error) {
	provider, err := state.Open(ctx, database)
	if err != nil {
		return nil, fmt.Errorf("open Backoffice state: %w", err)
	}
	bootstrap, err := NewBootstrapService(provider.Users())
	if err != nil {
		_ = provider.Close()
		return nil, err
	}
	return &Maintenance{provider: provider, bootstrap: bootstrap}, nil
}

func (m *Maintenance) Close() error { return m.provider.Close() }

func (m *Maintenance) BootstrapAdmin(ctx context.Context, input BootstrapInput) (BootstrapResult, error) {
	return m.bootstrap.Bootstrap(ctx, input)
}

func (m *Maintenance) Recover2FA(ctx context.Context, input RecoveryInput) (RecoveryResult, error) {
	service, err := NewRecoveryService(m.provider.Users())
	if err != nil {
		return RecoveryResult{}, err
	}
	return service.Recover(ctx, input)
}
