package auth

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type Collaborator = authcmd.Collaborator

type collaboratorStore interface {
	AddCollaborator(ctx context.Context, c Collaborator) error
	RemoveCollaborator(ctx context.Context, userID string) error
	GetCollaborator(ctx context.Context, userID string) (*Collaborator, bool, error)
	ListCollaborators(ctx context.Context) ([]Collaborator, error)
}

type CollaboratorStore struct {
	store     collaboratorStore
	canonical canonicalUserStore
}

func NewCollaboratorStore(store collaboratorStore) *CollaboratorStore {
	if store == nil {
		return nil
	}
	return &CollaboratorStore{store: store}
}

// NewCanonicalCollaboratorStore creates the transitional collaborator adapter backed only by canonical users.
func NewCanonicalCollaboratorStore(store canonicalUserStore) *CollaboratorStore {
	if store == nil {
		return nil
	}
	return &CollaboratorStore{canonical: store}
}

func (s *CollaboratorStore) AddCollaborator(ctx context.Context, c Collaborator) error {
	if s.canonical != nil {
		displayName := strings.TrimSpace(strings.Join([]string{c.FirstName, c.Username}, " "))
		_, err := attachCanonicalBinding(ctx, s.canonical, usercmd.RoleOperator, c.UserID, displayName, "verified-collaborator-invite")
		return err
	}
	if s.store == nil {
		return nil
	}
	return s.store.AddCollaborator(ctx, c)
}

func (s *CollaboratorStore) RemoveCollaborator(ctx context.Context, userID string) error {
	if s.canonical != nil {
		trimmed := strings.TrimSpace(userID)
		user, found, err := s.canonical.GetUser(ctx, trimmed)
		if err != nil {
			return err
		}
		if !found {
			channelType, principal, parseErr := canonicalSubject(trimmed)
			if parseErr == nil {
				user, found, err = s.canonical.GetUserByBinding(ctx, channelType, principal)
			}
			if err != nil {
				return err
			}
		}
		if !found || user.Role != usercmd.RoleOperator {
			return usercmd.ErrNotFound
		}
		if user.Status == usercmd.StatusDisabled {
			return nil
		}
		now := time.Now().UTC()
		beforeVersion := user.Version
		user.Status = usercmd.StatusDisabled
		user.Version++
		user.UpdatedAt = now
		audit := canonicalAudit(usercmd.AuditActionUserStatusChanged, usercmd.AuditTargetUser, user.ID, "bot collaborator removed", now)
		return s.canonical.UpdateUser(ctx, user, beforeVersion, audit)
	}
	if s.store == nil {
		return nil
	}
	return s.store.RemoveCollaborator(ctx, userID)
}

func (s *CollaboratorStore) GetCollaborator(ctx context.Context, userID string) (*Collaborator, bool, error) {
	if s.canonical != nil {
		channelType, principal, err := canonicalSubject(userID)
		if err != nil {
			return nil, false, nil
		}
		user, found, err := s.canonical.GetUserByBinding(ctx, channelType, principal)
		if err != nil || !found || !hasCanonicalCapability(user, usercmd.BotCapabilityCollaborator) {
			return nil, false, err
		}
		return canonicalCollaborator(user), true, nil
	}
	if s.store == nil {
		return nil, false, nil
	}
	return s.store.GetCollaborator(ctx, userID)
}

func (s *CollaboratorStore) ListCollaborators(ctx context.Context) ([]Collaborator, error) {
	if s.canonical != nil {
		all, err := canonicalUsers(ctx, s.canonical)
		if err != nil {
			return nil, err
		}
		result := make([]Collaborator, 0, len(all))
		for _, user := range all {
			if hasCanonicalCapability(user, usercmd.BotCapabilityCollaborator) {
				result = append(result, *canonicalCollaborator(user))
			}
		}
		return result, nil
	}
	if s.store == nil {
		return nil, nil
	}
	return s.store.ListCollaborators(ctx)
}

func canonicalCollaborator(user usercmd.User) *Collaborator {
	if user.Binding == nil {
		return nil
	}
	return &Collaborator{
		UserID: user.ID, Username: user.Username, FirstName: user.DisplayName,
		AddedBy: fmt.Sprintf("%s:%s", user.Binding.ChannelType, user.Binding.Principal), AddedAt: user.Binding.CreatedAt,
	}
}
