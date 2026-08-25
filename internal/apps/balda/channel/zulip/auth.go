package zulip

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
)

// OwnerStore captures the owner-facing store operations Zulip auth flows need.
type OwnerStore interface {
	HasOwner() bool
	GetOwner() *auth.Owner
	OwnerSubjects() []string
	IsOwnerSubject(subject string) bool
	RegisterOwnerSubject(subject string) (bool, error)
}

// InviteStore captures the invite-facing store operations Zulip auth flows need.
type InviteStore interface {
	GetInvite(ctx context.Context, token string) (*auth.Invite, error)
	CreateInvite(ctx context.Context, createdBy string) (string, *auth.Invite, error)
	ListInvites(ctx context.Context) ([]auth.Invite, error)
}

// CollaboratorStore captures the collaborator-facing store operations Zulip auth flows need.
type CollaboratorStore interface {
	GetCollaborator(ctx context.Context, userID string) (*auth.Collaborator, bool, error)
	AddCollaborator(ctx context.Context, collaborator auth.Collaborator) error
	ListCollaborators(ctx context.Context) ([]auth.Collaborator, error)
	RemoveCollaborator(ctx context.Context, userID string) error
}

// ChannelAuthService captures the owner-bind token flow used by Zulip auth.
type ChannelAuthService interface {
	ConsumeOwnerBind(ctx context.Context, channel, subject, token string) (bool, error)
}

// InitOwnerID resolves the initial Zulip owner ID from owner subjects or the owner record.
func InitOwnerID(store OwnerStore) (int64, bool) {
	if isNilInterface(store) || !store.HasOwner() {
		return 0, false
	}
	owner := store.GetOwner()
	if owner == nil {
		return 0, false
	}
	for _, subject := range store.OwnerSubjects() {
		value := strings.TrimPrefix(strings.TrimSpace(subject), auth.ChannelZulip+":")
		if value == subject || value == "" {
			continue
		}
		var id int
		if _, err := fmt.Sscanf(value, "%d", &id); err == nil && id > 0 {
			return int64(id), true
		}
	}
	return owner.UserID, true
}

// ConsumeOwnerBindToken consumes a Zulip owner-bind token for one sender.
func ConsumeOwnerBindToken(ctx context.Context, channelAuth ChannelAuthService, senderID int, token string) (bool, error) {
	if channelAuth == nil {
		return false, fmt.Errorf("token authentication is unavailable")
	}
	return channelAuth.ConsumeOwnerBind(ctx, auth.ChannelZulip, auth.ZulipSubject(senderID), token)
}

// RegisterOwner registers the Zulip sender as owner using the provided bootstrap token.
func RegisterOwner(store OwnerStore, senderID int, expectedToken string, providedToken string) (bool, error) {
	if isNilInterface(store) {
		return false, fmt.Errorf("owner store is unavailable")
	}
	if strings.TrimSpace(providedToken) != strings.TrimSpace(expectedToken) {
		return false, fmt.Errorf("invalid authentication token")
	}
	return store.RegisterOwnerSubject(auth.ZulipSubject(senderID))
}

// CanAccessCollaboratorScope reports whether the sender can use owner/collaborator scope.
func CanAccessCollaboratorScope(ctx context.Context, ownerStore OwnerStore, collaboratorStore CollaboratorStore, userID int64) (bool, error) {
	if !isNilInterface(ownerStore) && ownerStore.IsOwnerSubject(auth.ZulipSubject(int(userID))) {
		return true, nil
	}
	if isNilInterface(collaboratorStore) {
		return false, nil
	}
	_, found, err := collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	return found, err
}

// ConsumeInvite registers a collaborator from a Zulip invite token.
func ConsumeInvite(ctx context.Context, ownerStore OwnerStore, inviteStore InviteStore, collaboratorStore CollaboratorStore, senderID int, token string) (string, error) {
	userIDStr := fmt.Sprintf("%d", senderID)
	if isNilInterface(ownerStore) {
		return "", fmt.Errorf("owner store is unavailable")
	}
	if ownerStore.IsOwnerSubject(auth.ZulipSubject(senderID)) {
		return "You are already the bot owner.", nil
	}
	if !isNilInterface(collaboratorStore) {
		if _, ok, err := collaboratorStore.GetCollaborator(ctx, userIDStr); err != nil {
			return "", err
		} else if ok {
			return "You are already a collaborator.", nil
		}
	}
	if isNilInterface(inviteStore) || isNilInterface(collaboratorStore) {
		return "", fmt.Errorf("invite flow is unavailable")
	}
	invite, err := inviteStore.GetInvite(ctx, token)
	if err != nil {
		return "", err
	}
	if invite == nil {
		return "This invite token is invalid or has expired.", nil
	}
	collaborator := auth.Collaborator{
		UserID:  userIDStr,
		AddedBy: invite.CreatedBy,
		AddedAt: time.Now(),
	}
	if err := collaboratorStore.AddCollaborator(ctx, collaborator); err != nil {
		return "", err
	}
	return "Welcome! You are now a bot collaborator.", nil
}

// CreateInviteToken creates a collaborator invite token for the given owner sender.
func CreateInviteToken(ctx context.Context, inviteStore InviteStore, senderID int) (string, error) {
	if isNilInterface(inviteStore) {
		return "", fmt.Errorf("invite store is unavailable")
	}
	ownerIDStr := fmt.Sprintf("%d", senderID)
	token, _, err := inviteStore.CreateInvite(ctx, ownerIDStr)
	if err != nil {
		return "", err
	}
	return token, nil
}

// LoadUserListView loads collaborators and active invites for Zulip /user list.
func LoadUserListView(ctx context.Context, collaboratorStore CollaboratorStore, inviteStore InviteStore) ([]auth.Collaborator, []auth.Invite, error) {
	if isNilInterface(collaboratorStore) {
		return nil, nil, fmt.Errorf("collaborator store is unavailable")
	}
	collaborators, err := collaboratorStore.ListCollaborators(ctx)
	if err != nil {
		return nil, nil, err
	}
	var invites []auth.Invite
	if !isNilInterface(inviteStore) {
		invites, err = inviteStore.ListInvites(ctx)
		if err != nil {
			return nil, nil, err
		}
	}
	return collaborators, invites, nil
}

// RemoveCollaborator removes a Zulip collaborator by user ID.
func RemoveCollaborator(ctx context.Context, collaboratorStore CollaboratorStore, userID string) error {
	if isNilInterface(collaboratorStore) {
		return fmt.Errorf("collaborator store is unavailable")
	}
	trimmed := strings.TrimSpace(userID)
	if trimmed == "" {
		return fmt.Errorf("user id is required")
	}
	return collaboratorStore.RemoveCollaborator(ctx, trimmed)
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	rv := reflect.ValueOf(value)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
