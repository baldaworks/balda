package backoffice

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestStateReadinessUsesCanonicalUsers(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	service, err := NewStateService(&fakeStateUserStore{users: []usercmd.User{
		bootstrapTestUser("primary", usercmd.CredentialStateActive, true, now),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.ValidateReady(t.Context()); err != nil {
		t.Fatalf("canonical administrator readiness: %v", err)
	}
}

func TestValidateStateRequiresBootstrapOnFreshDatabase(t *testing.T) {
	t.Parallel()
	service := &StateService{users: &fakeStateUserStore{}}
	if err := service.ValidateReady(t.Context()); !errors.Is(err, ErrAdministratorBootstrapRequired) {
		t.Fatalf("ValidateReady() error = %v, want ErrAdministratorBootstrapRequired", err)
	}
}

func TestValidateStateRequiresCredentialBootstrap(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	service := &StateService{users: &fakeStateUserStore{users: []usercmd.User{
		bootstrapTestUser("primary", usercmd.CredentialStateDisabled, true, now),
	}}}
	if err := service.ValidateReady(t.Context()); !errors.Is(err, ErrAdministratorBootstrapRequired) {
		t.Fatalf("ValidateReady() error = %v, want ErrAdministratorBootstrapRequired", err)
	}
}

type fakeStateUserStore struct {
	users []usercmd.User
}

func (s *fakeStateUserStore) ListUsers(_ context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	if page.AfterID != "" {
		return usercmd.UserPage{}, nil
	}
	return usercmd.UserPage{Users: append([]usercmd.User(nil), s.users...)}, nil
}
