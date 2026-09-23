// Package access owns Backoffice-only canonical-user administration use cases.
package access

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

type store interface {
	CreateUser(ctx context.Context, user usercmd.User, secret usercmd.CredentialSecret, audit usercmd.AuditEvent) error
	UpdateUser(ctx context.Context, user usercmd.User, expectedVersion uint64, audit usercmd.AuditEvent) error
	ChangeCredential(ctx context.Context, userID string, expectedUserVersion, expectedCredentialVersion uint64, credential usercmd.Credential, secret usercmd.CredentialSecret, revokedAt time.Time, audit usercmd.AuditEvent) error
	GetUser(ctx context.Context, userID string) (usercmd.User, bool, error)
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
	GetSession(ctx context.Context, sessionID string) (usercmd.SessionFamily, bool, error)
	ListSessions(ctx context.Context, userID string, page usercmd.PageRequest) (usercmd.SessionPage, error)
	RevokeSession(ctx context.Context, sessionID string, expectedVersion uint64, revokedAt time.Time, reason string, audit usercmd.AuditEvent) error
}

// Actor is the authenticated administrator performing an Access mutation.
type Actor struct {
	User      usercmd.User
	SessionID string
}

// CreateInput contains the validated form values for canonical user creation.
type CreateInput struct {
	DisplayName       string
	Username          string
	TemporaryPassword []byte
	Role              usercmd.Role
	Status            usercmd.UserStatus
}

// UpdateInput contains one optimistic canonical user update.
type UpdateInput struct {
	UserID                     string
	DisplayName                string
	Username                   string
	Role                       usercmd.Role
	Status                     usercmd.UserStatus
	ExpectedVersion            uint64
	AcknowledgeBotAccessImpact bool
}

// CredentialInput contains one optimistic administrative credential reset.
type CredentialInput struct {
	UserID                    string
	ExpectedUserVersion       uint64
	ExpectedCredentialVersion uint64
	TemporaryPassword         []byte
	ConfirmCurrent            bool
}

// Service administers canonical users and browser session families.
type Service struct {
	store store
	now   func() time.Time
	newID func() string
}

// NewService creates the Backoffice Access use case over its local persistence port.
func NewService(store store) *Service {
	return &Service{store: store, now: time.Now, newID: uuid.NewString}
}

func (s *Service) ListUsers(ctx context.Context, actor Actor) ([]usercmd.User, error) {
	if err := requireAccessAdministrator(actor); err != nil {
		return nil, err
	}
	return s.listAllUsers(ctx)
}

func (s *Service) GetUser(ctx context.Context, actor Actor, userID string) (usercmd.User, error) {
	if err := requireAccessAdministrator(actor); err != nil {
		return usercmd.User{}, err
	}
	user, found, err := s.store.GetUser(ctx, strings.TrimSpace(userID))
	if err != nil {
		return usercmd.User{}, fmt.Errorf("load access user: %w", err)
	}
	if !found {
		return usercmd.User{}, usercmd.ErrNotFound
	}
	return user, nil
}

func (s *Service) CreateUser(ctx context.Context, actor Actor, input CreateInput) (usercmd.User, error) {
	if err := requireAccessAdministrator(actor); err != nil {
		return usercmd.User{}, err
	}
	password := append([]byte(nil), input.TemporaryPassword...)
	defer clear(password)
	hash, err := userpassword.Hash(password)
	if err != nil {
		if errors.Is(err, userpassword.ErrInvalidPassword) {
			return usercmd.User{}, fmt.Errorf("%w: temporary password length is invalid", usercmd.ErrInvalid)
		}
		return usercmd.User{}, err
	}
	now := s.now().UTC()
	user := usercmd.User{
		ID: s.newID(), DisplayName: strings.TrimSpace(input.DisplayName), Username: strings.TrimSpace(input.Username),
		NormalizedUsername: users.NormalizeUsername(input.Username), Status: input.Status, Role: input.Role,
		Credential: usercmd.Credential{State: usercmd.CredentialStateTemporary, MustChange: true, Version: 1},
		Version:    1, CreatedAt: now, UpdatedAt: now,
	}
	if err := users.ValidateUser(user); err != nil {
		return usercmd.User{}, err
	}
	audit := s.audit(actor, usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, user.ID, "canonical user created", now)
	if err := s.store.CreateUser(ctx, user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash}, audit); err != nil {
		return usercmd.User{}, fmt.Errorf("create access user: %w", err)
	}
	return user, nil
}

func (s *Service) UpdateUser(ctx context.Context, actor Actor, input UpdateInput) (usercmd.User, error) {
	before, err := s.GetUser(ctx, actor, input.UserID)
	if err != nil {
		return usercmd.User{}, err
	}
	allUsers, err := s.listAllUsers(ctx)
	if err != nil {
		return usercmd.User{}, err
	}
	activeAdministrators := 0
	for _, user := range allUsers {
		if user.Role == usercmd.RoleAdministrator && user.Status == usercmd.StatusActive {
			activeAdministrators++
		}
	}
	change := users.RoleStatusChange{
		Before: before, NextRole: input.Role, NextStatus: input.Status,
		ExpectedVersion: input.ExpectedVersion, ActiveAdministrators: activeAdministrators,
		AcknowledgeBotAccessImpact: input.AcknowledgeBotAccessImpact,
	}
	if err := users.ValidateRoleStatusChange(change); err != nil {
		return usercmd.User{}, err
	}
	now := s.now().UTC()
	next := before
	next.DisplayName = strings.TrimSpace(input.DisplayName)
	next.Username = strings.TrimSpace(input.Username)
	next.NormalizedUsername = users.NormalizeUsername(input.Username)
	next.Role = input.Role
	next.Status = input.Status
	next.Version++
	next.UpdatedAt = now
	if err := users.ValidateUser(next); err != nil {
		return usercmd.User{}, err
	}
	action := usercmd.AuditActionUserUpdated
	reason := "canonical user profile updated"
	switch {
	case before.Role != next.Role && before.Status != next.Status:
		action = usercmd.AuditActionUserAccessChanged
		reason = "canonical user role and status changed"
	case before.Role != next.Role:
		action = usercmd.AuditActionUserRoleChanged
		reason = "canonical user role changed"
	case before.Status != next.Status:
		action = usercmd.AuditActionUserStatusChanged
		reason = "canonical user status changed"
	}
	if err := s.store.UpdateUser(ctx, next, input.ExpectedVersion, s.audit(actor, action, usercmd.AuditTargetUser, next.ID, reason, now)); err != nil {
		return usercmd.User{}, fmt.Errorf("update access user: %w", err)
	}
	return next, nil
}

func (s *Service) ResetCredential(ctx context.Context, actor Actor, input CredentialInput) error {
	before, err := s.GetUser(ctx, actor, input.UserID)
	if err != nil {
		return err
	}
	nextCredential := usercmd.Credential{
		State: usercmd.CredentialStateTemporary, MustChange: true,
		Version: before.Credential.Version + 1,
	}
	if err := users.ValidateCredentialChange(users.CredentialChange{
		Before: before, Next: nextCredential, ExpectedUserVersion: input.ExpectedUserVersion,
		ExpectedCredentialVersion: input.ExpectedCredentialVersion,
	}); err != nil {
		return err
	}
	if actor.User.ID == before.ID {
		if err := users.ValidateSessionRevocation(actor.SessionID, actor.SessionID, input.ConfirmCurrent); err != nil {
			return err
		}
	}
	password := append([]byte(nil), input.TemporaryPassword...)
	defer clear(password)
	hash, err := userpassword.Hash(password)
	if err != nil {
		if errors.Is(err, userpassword.ErrInvalidPassword) {
			return fmt.Errorf("%w: temporary password length is invalid", usercmd.ErrInvalid)
		}
		return err
	}
	now := s.now().UTC()
	audit := s.audit(actor, usercmd.AuditActionCredentialChanged, usercmd.AuditTargetUser, before.ID, "administrator reset temporary credential", now)
	if err := s.store.ChangeCredential(
		ctx, before.ID, input.ExpectedUserVersion, input.ExpectedCredentialVersion, nextCredential,
		usercmd.CredentialSecret{UserID: before.ID, PasswordHash: hash}, now, audit,
	); err != nil {
		return fmt.Errorf("reset access credential: %w", err)
	}
	return nil
}

func (s *Service) ListSessions(ctx context.Context, actor Actor, userID string) (usercmd.SessionPage, error) {
	if _, err := s.GetUser(ctx, actor, userID); err != nil {
		return usercmd.SessionPage{}, err
	}
	page, err := s.store.ListSessions(ctx, strings.TrimSpace(userID), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		return usercmd.SessionPage{}, fmt.Errorf("list access sessions: %w", err)
	}
	return page, nil
}

func (s *Service) RevokeSession(ctx context.Context, actor Actor, userID, sessionID string, confirmCurrent bool) error {
	if _, err := s.GetUser(ctx, actor, userID); err != nil {
		return err
	}
	target, found, err := s.store.GetSession(ctx, strings.TrimSpace(sessionID))
	if err != nil {
		return fmt.Errorf("load access session: %w", err)
	}
	if !found || target.UserID != strings.TrimSpace(userID) {
		return usercmd.ErrNotFound
	}
	if err := users.ValidateSessionRevocation(actor.SessionID, target.ID, confirmCurrent); err != nil {
		return err
	}
	now := s.now().UTC()
	audit := s.audit(actor, usercmd.AuditActionSessionRevoked, usercmd.AuditTargetSession, target.ID, "administrator revoked browser session", now)
	if err := s.store.RevokeSession(ctx, target.ID, target.Version, now, "administrative revocation", audit); err != nil {
		return fmt.Errorf("revoke access session: %w", err)
	}
	return nil
}

func (s *Service) listAllUsers(ctx context.Context) ([]usercmd.User, error) {
	var result []usercmd.User
	page := usercmd.PageRequest{Limit: usercmd.MaxPageSize}
	for {
		usersPage, err := s.store.ListUsers(ctx, page)
		if err != nil {
			return nil, fmt.Errorf("list access users: %w", err)
		}
		result = append(result, usersPage.Users...)
		if usersPage.NextAfterID == "" {
			return result, nil
		}
		page.AfterID = usersPage.NextAfterID
	}
}

func (s *Service) audit(actor Actor, action usercmd.AuditAction, targetType usercmd.AuditTargetType, targetID, reason string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: s.newID(), Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		ActorUserID: actor.User.ID, ActorSessionID: actor.SessionID,
		TargetType: targetType, TargetID: targetID, Reason: reason,
		Source: "backoffice-access", OccurredAt: now,
	}
}

func requireAccessAdministrator(actor Actor) error {
	capabilities := users.BackofficeCapabilities(actor.User)
	if !capabilities.ManageUsers || strings.TrimSpace(actor.SessionID) == "" {
		return usercmd.ErrForbidden
	}
	return nil
}
