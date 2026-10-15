package backoffice

import (
	"context"
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/google/uuid"
)

type recoveryStore interface {
	GetUserByNormalizedUsername(ctx context.Context, username string) (usercmd.User, bool, error)
	GetMFAProfile(ctx context.Context, userID string) (usercmd.MFAProfile, error)
	ApplyMFAChange(ctx context.Context, change usercmd.MFAChange) error
}

// RecoveryInput selects exactly one normalized administrator and confirms opt-out.
type RecoveryInput struct {
	Username string
	Confirm  bool
}

// RecoveryResult contains no credential or newly issued browser authorization.
type RecoveryResult struct{ Username string }

// RecoveryService owns the explicitly confirmed host-only factor removal operation.
type RecoveryService struct {
	store recoveryStore
	now   func() time.Time
	newID func() string
}

// NewRecoveryService constructs maintenance over a narrow canonical storage port.
func NewRecoveryService(store recoveryStore) (*RecoveryService, error) {
	if store == nil {
		return nil, fmt.Errorf("recovery store is required")
	}
	return &RecoveryService{store: store, now: time.Now, newID: uuid.NewString}, nil
}

// Recover disables MFA without changing passwords, bot bindings or issuing sessions.
func (s *RecoveryService) Recover(ctx context.Context, input RecoveryInput) (RecoveryResult, error) {
	if !input.Confirm || input.Username == "" || len(input.Username) > 128 || input.Username != users.NormalizeUsername(input.Username) {
		return RecoveryResult{}, usercmd.ErrInvalid
	}
	u, found, err := s.store.GetUserByNormalizedUsername(ctx, input.Username)
	if err != nil {
		return RecoveryResult{}, err
	}
	if !found {
		return RecoveryResult{}, usercmd.ErrNotFound
	}
	if u.Role != usercmd.RoleAdministrator || u.Status != usercmd.StatusActive || u.Credential.State == usercmd.CredentialStateDisabled {
		return RecoveryResult{}, usercmd.ErrForbidden
	}
	profile, err := s.store.GetMFAProfile(ctx, u.ID)
	if err != nil {
		return RecoveryResult{}, err
	}
	if !profile.Enabled {
		return RecoveryResult{}, fmt.Errorf("%w: administrator has no enabled second factor", usercmd.ErrInvalid)
	}
	now := s.now().UTC()
	audit := usercmd.AuditEvent{ID: s.newID(), Action: usercmd.AuditActionMFARecovered, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: u.ID, Source: "backoffice-offline-recovery", Reason: "explicitly confirmed factor removal", OccurredAt: now}
	if err := s.store.ApplyMFAChange(ctx, usercmd.MFAChange{UserID: u.ID, ExpectedUserVersion: u.Version,
		ExpectedCredentialVersion: u.Credential.Version, ExpectedMFAVersion: profile.Version, Purpose: usercmd.MFARecover, ChangedAt: now, Audit: audit}); err != nil {
		return RecoveryResult{}, fmt.Errorf("recover administrator factor: %w", err)
	}
	return RecoveryResult{Username: u.NormalizedUsername}, nil
}
