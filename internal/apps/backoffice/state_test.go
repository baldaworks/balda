package backoffice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestValidateStateRequiresMigrationBeforeBootstrap(t *testing.T) {
	t.Parallel()
	service := &StateService{
		owner:         &fakeLegacyOwnerReader{raw: map[string]any{"user_id": float64(101)}},
		collaborators: &fakeLegacyCollaboratorReader{},
		users:         &fakeStateUserStore{},
	}
	if err := service.ValidateReady(t.Context()); !errors.Is(err, ErrUserMigrationRequired) {
		t.Fatalf("ValidateReady() error = %v, want ErrUserMigrationRequired", err)
	}
}

func TestValidateStateRequiresBootstrapOnFreshDatabase(t *testing.T) {
	t.Parallel()
	service := &StateService{
		owner: &fakeLegacyOwnerReader{}, collaborators: &fakeLegacyCollaboratorReader{}, users: &fakeStateUserStore{},
	}
	if err := service.ValidateReady(t.Context()); !errors.Is(err, ErrAdministratorBootstrapRequired) {
		t.Fatalf("ValidateReady() error = %v, want ErrAdministratorBootstrapRequired", err)
	}
}

func TestValidateStateAcceptsMigratedCanonicalPrimary(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	service := &StateService{
		owner:         &fakeLegacyOwnerReader{raw: map[string]any{"user_id": float64(101)}},
		collaborators: &fakeLegacyCollaboratorReader{},
		users: &fakeStateUserStore{migrated: true, users: []usercmd.User{
			bootstrapTestUser("primary", usercmd.CredentialStateTemporary, true, now),
		}},
	}
	if err := service.ValidateReady(t.Context()); err != nil {
		t.Fatalf("ValidateReady() error = %v", err)
	}
}

func TestValidateStateRequiresCredentialBootstrap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	service := &StateService{
		owner: &fakeLegacyOwnerReader{}, collaborators: &fakeLegacyCollaboratorReader{},
		users: &fakeStateUserStore{users: []usercmd.User{
			bootstrapTestUser("primary", usercmd.CredentialStateDisabled, true, now),
		}},
	}
	if err := service.ValidateReady(t.Context()); !errors.Is(err, ErrAdministratorBootstrapRequired) {
		t.Fatalf("ValidateReady() error = %v, want ErrAdministratorBootstrapRequired", err)
	}
}

type fakeLegacyOwnerReader struct {
	raw any
	err error
}

func (s *fakeLegacyOwnerReader) GetJSON(context.Context, string) (any, bool, error) {
	return s.raw, s.raw != nil, s.err
}

type fakeLegacyCollaboratorReader struct {
	users []authcmd.Collaborator
	err   error
}

func (s *fakeLegacyCollaboratorReader) ListCollaborators(context.Context) ([]authcmd.Collaborator, error) {
	return append([]authcmd.Collaborator(nil), s.users...), s.err
}

type fakeStateUserStore struct {
	users    []usercmd.User
	migrated bool
}

func (s *fakeStateUserStore) ListUsers(_ context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	if page.AfterID != "" {
		return usercmd.UserPage{}, nil
	}
	return usercmd.UserPage{Users: append([]usercmd.User(nil), s.users...)}, nil
}

func (s *fakeStateUserStore) AnyUserMigrationApplied(context.Context) (bool, error) {
	return s.migrated, nil
}

func (s *fakeStateUserStore) UserMigrationApplied(context.Context, string) (bool, error) {
	return s.migrated, nil
}

func (s *fakeStateUserStore) ApplyUserMigration(context.Context, usercmd.UserMigration) (bool, error) {
	s.migrated = true
	return true, nil
}
