package backoffice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/google/uuid"
)

// ErrCredentialResetRequired prevents accidental replacement of a usable credential.
var ErrCredentialResetRequired = errors.New("explicit credential reset is required")

type bootstrapStore interface {
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
	CreateUser(ctx context.Context, user usercmd.User, secret usercmd.CredentialSecret, audit usercmd.AuditEvent) error
	ChangeCredential(ctx context.Context, userID string, expectedUserVersion, expectedCredentialVersion uint64, credential usercmd.Credential, secret usercmd.CredentialSecret, revokedAt time.Time, audit usercmd.AuditEvent) error
}

// BootstrapInput contains secret-safe target options and a transient stdin password.
type BootstrapInput struct {
	UserID      string
	Username    string
	DisplayName string
	Password    []byte
	Reset       bool
}

// BootstrapResult reports only non-secret administrator setup details.
type BootstrapResult struct {
	UserID   string
	Username string
	Created  bool
}

// BootstrapService establishes or explicitly resets local administrator credentials.
type BootstrapService struct {
	store bootstrapStore
	hash  func([]byte) (string, error)
	now   func() time.Time
	newID func() string
}

// NewBootstrapService creates an administrator bootstrap service.
func NewBootstrapService(store bootstrapStore) (*BootstrapService, error) {
	if store == nil {
		return nil, fmt.Errorf("bootstrap user store is required")
	}
	return &BootstrapService{store: store, hash: userpassword.Hash, now: time.Now, newID: uuid.NewString}, nil
}

// Bootstrap creates a fresh primary administrator or updates the selected existing administrator.
func (s *BootstrapService) Bootstrap(ctx context.Context, input BootstrapInput) (BootstrapResult, error) {
	password := append([]byte(nil), input.Password...)
	defer func() {
		for i := range password {
			password[i] = 0
		}
	}()
	all, err := listBootstrapUsers(ctx, s.store)
	if err != nil {
		return BootstrapResult{}, fmt.Errorf("list bootstrap users: %w", err)
	}
	selection, err := users.SelectBootstrapUser(all, input.UserID)
	if err != nil {
		return BootstrapResult{}, err
	}
	var target usercmd.User
	if selection.Action != users.BootstrapCreatePrimary {
		target = findBootstrapUser(all, selection.UserID)
		if target.ID == "" {
			return BootstrapResult{}, usercmd.ErrNotFound
		}
		if target.Credential.State != usercmd.CredentialStateDisabled && !input.Reset {
			return BootstrapResult{}, ErrCredentialResetRequired
		}
	}
	hash, err := s.hash(password)
	if err != nil {
		return BootstrapResult{}, err
	}
	now := s.now().UTC()
	if selection.Action == users.BootstrapCreatePrimary {
		username := strings.TrimSpace(input.Username)
		if username == "" {
			username = "admin"
		}
		displayName := strings.TrimSpace(input.DisplayName)
		if displayName == "" {
			displayName = "Administrator"
		}
		user := usercmd.User{
			ID: s.newID(), DisplayName: displayName, Username: username, NormalizedUsername: users.NormalizeUsername(username),
			Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator,
			Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1},
			Primary:    true, Version: 1, CreatedAt: now, UpdatedAt: now,
		}
		audit := bootstrapAudit(user.ID, "fresh administrator bootstrap", now, s.newID())
		if err := s.store.CreateUser(ctx, user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
			return BootstrapResult{}, fmt.Errorf("create bootstrap administrator: %w", err)
		}
		return BootstrapResult{UserID: user.ID, Username: user.Username, Created: true}, nil
	}
	credential := usercmd.Credential{State: usercmd.CredentialStateActive, Version: target.Credential.Version + 1}
	audit := bootstrapAudit(target.ID, "administrator credential bootstrap", now, s.newID())
	if err := s.store.ChangeCredential(
		ctx, target.ID, target.Version, target.Credential.Version,
		credential, usercmd.CredentialSecret{UserID: target.ID, PasswordHash: hash}, now, audit,
	); err != nil {
		return BootstrapResult{}, fmt.Errorf("set bootstrap administrator credential: %w", err)
	}
	return BootstrapResult{UserID: target.ID, Username: target.Username}, nil
}

func listBootstrapUsers(ctx context.Context, store bootstrapStore) ([]usercmd.User, error) {
	var result []usercmd.User
	page := usercmd.PageRequest{Limit: usercmd.MaxPageSize}
	for {
		usersPage, err := store.ListUsers(ctx, page)
		if err != nil {
			return nil, err
		}
		result = append(result, usersPage.Users...)
		if usersPage.NextAfterID == "" {
			return result, nil
		}
		page.AfterID = usersPage.NextAfterID
	}
}

func findBootstrapUser(all []usercmd.User, userID string) usercmd.User {
	for _, user := range all {
		if user.ID == userID {
			return user
		}
	}
	return usercmd.User{}
}

func bootstrapAudit(userID, reason string, now time.Time, auditID string) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: auditID, Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetUser, TargetID: userID, Reason: reason,
		Source: "backoffice-bootstrap", OccurredAt: now,
	}
}
