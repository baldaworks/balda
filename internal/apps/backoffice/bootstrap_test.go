package backoffice

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestBootstrapCreatesFreshPrimaryAdministrator(t *testing.T) {
	t.Parallel()
	store := &fakeBootstrapStore{}
	service := newTestBootstrapService(t, store)
	result, err := service.Bootstrap(t.Context(), BootstrapInput{
		Username: "Admin", DisplayName: "Primary Administrator", Password: []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.UserID == "" || result.Username != "Admin" {
		t.Fatalf("Bootstrap() = %+v", result)
	}
	if len(store.users) != 1 || !store.users[0].Primary || store.users[0].Role != usercmd.RoleAdministrator {
		t.Fatalf("created users = %+v", store.users)
	}
	if store.secret.PasswordHash != "hash:28" || store.audit.TargetID != result.UserID {
		t.Fatalf("stored secret/audit = %+v / %+v", store.secret, store.audit)
	}
}

func TestBootstrapExistingCredentialRequiresExplicitReset(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 7, 0, 0, 0, time.UTC)
	store := &fakeBootstrapStore{users: []usercmd.User{bootstrapTestUser("primary", usercmd.CredentialStateTemporary, true, now)}}
	service := newTestBootstrapService(t, store)
	input := BootstrapInput{Password: []byte("correct horse battery staple")}
	if _, err := service.Bootstrap(t.Context(), input); !errors.Is(err, ErrCredentialResetRequired) {
		t.Fatalf("Bootstrap() error = %v, want ErrCredentialResetRequired", err)
	}
	input.Reset = true
	result, err := service.Bootstrap(t.Context(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || store.changed.ID != "primary" || store.changed.Credential.Version != 2 || store.changed.Version != 2 {
		t.Fatalf("reset result/user = %+v / %+v", result, store.changed)
	}
	if store.revokedAt.IsZero() {
		t.Fatal("credential reset did not request complete session revocation")
	}
}

func TestBootstrapSelectsExplicitAdministrator(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 23, 8, 0, 0, 0, time.UTC)
	store := &fakeBootstrapStore{users: []usercmd.User{
		bootstrapTestUser("primary", usercmd.CredentialStateTemporary, true, now),
		bootstrapTestUser("selected", usercmd.CredentialStateDisabled, false, now),
	}}
	service := newTestBootstrapService(t, store)
	result, err := service.Bootstrap(t.Context(), BootstrapInput{
		UserID: "selected", Password: []byte("correct horse battery staple"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.UserID != "selected" || store.changed.ID != "selected" {
		t.Fatalf("Bootstrap() = %+v, changed=%+v", result, store.changed)
	}
}

func newTestBootstrapService(t *testing.T, store *fakeBootstrapStore) *BootstrapService {
	t.Helper()
	service, err := NewBootstrapService(store)
	if err != nil {
		t.Fatal(err)
	}
	service.hash = func(password []byte) (string, error) { return "hash:" + strconv.Itoa(len(password)), nil }
	service.now = func() time.Time { return time.Date(2026, 9, 23, 9, 0, 0, 0, time.UTC) }
	service.newID = func() string { return "generated-id" }
	return service
}

type fakeBootstrapStore struct {
	users     []usercmd.User
	secret    usercmd.CredentialSecret
	audit     usercmd.AuditEvent
	changed   usercmd.User
	revokedAt time.Time
}

func (s *fakeBootstrapStore) ListUsers(_ context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	if page.AfterID != "" {
		return usercmd.UserPage{}, nil
	}
	return usercmd.UserPage{Users: append([]usercmd.User(nil), s.users...)}, nil
}

func (s *fakeBootstrapStore) CreateUser(_ context.Context, user usercmd.User, secret usercmd.CredentialSecret, audit usercmd.AuditEvent) error {
	s.users = append(s.users, user)
	s.secret = secret
	s.audit = audit
	return nil
}

func (s *fakeBootstrapStore) ChangeCredential(
	_ context.Context,
	_ string,
	_, _ uint64,
	credential usercmd.Credential,
	secret usercmd.CredentialSecret,
	revokedAt time.Time,
	audit usercmd.AuditEvent,
) error {
	for _, user := range s.users {
		if user.ID != secret.UserID {
			continue
		}
		user.Credential = credential
		user.Version++
		user.UpdatedAt = revokedAt
		s.changed = user
		s.secret = secret
		s.audit = audit
		s.revokedAt = revokedAt
		return nil
	}
	return usercmd.ErrNotFound
}

func bootstrapTestUser(id string, state usercmd.CredentialState, primary bool, now time.Time) usercmd.User {
	return usercmd.User{
		ID: id, DisplayName: id, Username: id, NormalizedUsername: id,
		Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
		Credential: usercmd.Credential{State: state, MustChange: state == usercmd.CredentialStateTemporary, Version: 1},
		Primary:    primary, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
}
