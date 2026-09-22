package auth

import (
	"context"
	"sort"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestCanonicalAuthorizationUsesCurrentUserState(t *testing.T) {
	t.Parallel()
	admin := canonicalTestUser("admin", usercmd.RoleAdministrator, usercmd.StatusActive, "telegram", "101", true)
	admin.Binding.Provenance = "legacy-owner;chat_id=909"
	store := newFakeCanonicalUserStore(
		admin,
		canonicalTestUser("operator", usercmd.RoleOperator, usercmd.StatusActive, "zulip", "202", false),
		canonicalTestUser("disabled", usercmd.RoleAdministrator, usercmd.StatusDisabled, "telegram", "303", false),
	)
	owners, err := NewCanonicalOwnerStore(store)
	if err != nil {
		t.Fatal(err)
	}
	collaborators := NewCanonicalCollaboratorStore(store)
	if !owners.IsOwner(101) || owners.IsOwner(303) || owners.IsOwnerSubject("zulip:202") {
		t.Fatal("owner capability did not follow canonical role and status")
	}
	if owner := owners.GetOwner(); owner == nil || owner.UserID != 101 || owner.ChatID != 909 {
		t.Fatalf("GetOwner() = %+v", owner)
	}
	if _, found, err := collaborators.GetCollaborator(t.Context(), "zulip:202"); err != nil || !found {
		t.Fatalf("GetCollaborator() found=%t error=%v", found, err)
	}
	if _, found, err := collaborators.GetCollaborator(t.Context(), "telegram:101"); err != nil || found {
		t.Fatalf("GetCollaborator(admin) found=%t error=%v", found, err)
	}
}

func TestCanonicalOnboardingAttachesOnlyPreexistingUnboundUser(t *testing.T) {
	t.Parallel()
	store := newFakeCanonicalUserStore(
		canonicalTestUser("primary-admin", usercmd.RoleAdministrator, usercmd.StatusActive, "", "", true),
		canonicalTestUser("operator", usercmd.RoleOperator, usercmd.StatusActive, "", "", false),
	)
	owners, err := NewCanonicalOwnerStore(store)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := owners.RegisterOwnerSubject("slackagent:T1:U1")
	if err != nil || !registered {
		t.Fatalf("RegisterOwnerSubject() = %t, %v", registered, err)
	}
	if !owners.IsOwnerSubject("slackagent:T1:U1") {
		t.Fatal("attached administrator is not authorized")
	}
	collaborators := NewCanonicalCollaboratorStore(store)
	if err := collaborators.AddCollaborator(t.Context(), Collaborator{UserID: "telegram:202", FirstName: "Operator"}); err != nil {
		t.Fatalf("AddCollaborator() error = %v", err)
	}
	listed, err := collaborators.ListCollaborators(t.Context())
	if err != nil || len(listed) != 1 || listed[0].UserID != "operator" {
		t.Fatalf("ListCollaborators() = %+v, %v", listed, err)
	}
	if err := collaborators.RemoveCollaborator(t.Context(), "operator"); err != nil {
		t.Fatalf("RemoveCollaborator() error = %v", err)
	}
	if _, found, err := collaborators.GetCollaborator(t.Context(), "telegram:202"); err != nil || found {
		t.Fatalf("disabled collaborator found=%t error=%v", found, err)
	}
}

type fakeCanonicalUserStore struct {
	users  map[string]usercmd.User
	claims map[string]usercmd.BindingClaim
}

func newFakeCanonicalUserStore(users ...usercmd.User) *fakeCanonicalUserStore {
	store := &fakeCanonicalUserStore{users: make(map[string]usercmd.User), claims: make(map[string]usercmd.BindingClaim)}
	for _, user := range users {
		store.users[user.ID] = user
	}
	return store
}

func (s *fakeCanonicalUserStore) GetUser(_ context.Context, userID string) (usercmd.User, bool, error) {
	user, found := s.users[userID]
	return user, found, nil
}

func (s *fakeCanonicalUserStore) GetUserByBinding(_ context.Context, channelType, principal string) (usercmd.User, bool, error) {
	for _, user := range s.users {
		if user.Binding != nil && user.Binding.ChannelType == channelType && user.Binding.Principal == principal {
			return user, true, nil
		}
	}
	return usercmd.User{}, false, nil
}

func (s *fakeCanonicalUserStore) ListUsers(_ context.Context, page usercmd.PageRequest) (usercmd.UserPage, error) {
	ids := make([]string, 0, len(s.users))
	for id := range s.users {
		if id > page.AfterID {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	result := usercmd.UserPage{}
	for _, id := range ids {
		result.Users = append(result.Users, s.users[id])
	}
	return result, nil
}

func (s *fakeCanonicalUserStore) CreateBindingClaim(_ context.Context, claim usercmd.BindingClaim, _ time.Time, _ usercmd.AuditEvent) error {
	s.claims[claim.ID] = claim
	return nil
}

func (s *fakeCanonicalUserStore) AttachBinding(_ context.Context, claimID string, binding usercmd.Binding, _ time.Time, _ usercmd.AuditEvent) error {
	claim, found := s.claims[claimID]
	if !found || claim.UserID != binding.UserID || claim.ChannelType != binding.ChannelType {
		return usercmd.ErrBindingClaimScope
	}
	user := s.users[binding.UserID]
	user.Binding = &binding
	s.users[user.ID] = user
	return nil
}

func (s *fakeCanonicalUserStore) UpdateUser(_ context.Context, user usercmd.User, expectedVersion uint64, _ usercmd.AuditEvent) error {
	current, found := s.users[user.ID]
	if !found {
		return usercmd.ErrNotFound
	}
	if current.Version != expectedVersion {
		return usercmd.ErrConflict
	}
	s.users[user.ID] = user
	return nil
}

func canonicalTestUser(id string, role usercmd.Role, status usercmd.UserStatus, channelType, principal string, primary bool) usercmd.User {
	now := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	user := usercmd.User{
		ID: id, DisplayName: id, Username: id, NormalizedUsername: id, Role: role, Status: status, Primary: primary,
		Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	if channelType != "" {
		user.Binding = &usercmd.Binding{
			ID: "binding-" + id, UserID: id, ChannelType: channelType, Principal: principal,
			DisplayName: id, Provenance: "test", CreatedAt: now, UpdatedAt: now,
		}
	}
	return user
}
