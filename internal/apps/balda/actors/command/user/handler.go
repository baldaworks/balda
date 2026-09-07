// Package user owns owner collaborator administration commands.
package user

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const (
	transportTelegram = "telegram"
	transportZulip    = "zulip"

	userActionAdd    = "add"
	userActionInvite = "invite"
	userActionList   = "list"
	userActionRemove = "remove"

	suffixNotOwner      = "user-not-owner"
	suffixUsage         = "user-usage"
	suffixAddError      = "user-add-error"
	suffixAddSuccess    = "user-add-success"
	suffixListError     = "user-list-error"
	suffixListSuccess   = "user-list-success"
	suffixRemoveError   = "user-remove-error"
	suffixRemoveSuccess = "user-remove-success"

	telegramUsage = "Usage:\n" +
		"• /user add - Generate invite link\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"

	defaultUsage = "Usage:\n" +
		"• /user add - Generate invite token\n" +
		"• /user list - Show collaborators and active invites\n" +
		"• /user remove <user_id> - Remove collaborator by ID\n"

	notOwnerMessage = "This command is only for the owner."
)

// OwnerStore defines owner verification operations.
type OwnerStore interface {
	IsOwner(userID int64) bool
	IsOwnerSubject(subject string) bool
}

// InviteStore defines invite lifecycle operations.
type InviteStore interface {
	CreateInvite(ctx context.Context, createdBy string) (string, *auth.Invite, error)
	ListInvites(ctx context.Context) ([]auth.Invite, error)
}

// CollaboratorStore defines collaborator management operations.
type CollaboratorStore interface {
	ListCollaborators(ctx context.Context) ([]auth.Collaborator, error)
	RemoveCollaborator(ctx context.Context, userID string) error
}

// BotUsernameProvider optionally retrieves the bot username for invite link generation.
type BotUsernameProvider interface {
	GetBotUsername(ctx context.Context) string
}

// Handler handles the /user command family.
type Handler struct {
	ownerStore        OwnerStore
	inviteStore       InviteStore
	collaboratorStore CollaboratorStore
	botUsernames      BotUsernameProvider
	dispatcher        actortransport.Dispatcher
	logger            zerolog.Logger
}

// New creates a new user command Handler.
func New(
	ownerStore OwnerStore,
	inviteStore InviteStore,
	collaboratorStore CollaboratorStore,
	botUsernames BotUsernameProvider,
	dispatcher actortransport.Dispatcher,
	logger zerolog.Logger,
) *Handler {
	return &Handler{
		ownerStore:        ownerStore,
		inviteStore:       inviteStore,
		collaboratorStore: collaboratorStore,
		botUsernames:      botUsernames,
		dispatcher:        dispatcher,
		logger:            logger.With().Str("component", "balda.actors.command.user").Logger(),
	}
}

// Name returns "user".
func (h *Handler) Name() string { return "user" }

// Handle executes the user command request.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !h.isOwner(p) {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, notOwnerMessage, suffixNotOwner)
	}

	args := strings.Fields(p.Args)
	if len(args) == 0 {
		return h.sendUsage(ctx, env.ID, p)
	}

	switch args[0] {
	case userActionAdd, userActionInvite:
		return h.onAdd(ctx, env.ID, p)
	case userActionList:
		return h.onList(ctx, env.ID, p)
	case userActionRemove:
		return h.onRemove(ctx, env.ID, p, args)
	default:
		return h.sendUsage(ctx, env.ID, p)
	}
}

func (h *Handler) sendUsage(ctx context.Context, opID string, p commandcmd.Payload) error {
	usage := defaultUsage
	if p.Transport == transportTelegram {
		usage = telegramUsage
	}
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, usage, suffixUsage)
}

func (h *Handler) onAdd(ctx context.Context, opID string, p commandcmd.Payload) error {
	if h.inviteStore == nil {
		return h.sendAddError(ctx, opID, p)
	}

	ownerID := strings.TrimSpace(p.Principal)
	token, _, err := h.inviteStore.CreateInvite(ctx, ownerID)
	if err != nil {
		h.logger.Error().Err(err).Str("principal", p.Principal).Msg("failed to create invite")
		return h.sendAddError(ctx, opID, p)
	}

	var message string
	if p.Transport == transportTelegram {
		username := ""
		if h.botUsernames != nil {
			username = strings.TrimSpace(h.botUsernames.GetBotUsername(ctx))
		}
		if username == "" {
			username = "<bot_username>"
		}
		inviteLink := fmt.Sprintf("https://t.me/%s?start=invite_%s", username, token)
		message = fmt.Sprintf("Invite link created:\n%s\n\nVisit this link to become a bot collaborator", inviteLink)
	} else {
		message = fmt.Sprintf("Invite token created:\n%s\n\nHave the collaborator send:\n/start invite=%s", token, token)
	}

	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, message, suffixAddSuccess)
}

func (h *Handler) sendAddError(ctx context.Context, opID string, p commandcmd.Payload) error {
	msg := "Failed to create invite token."
	if p.Transport == transportTelegram {
		msg = "Failed to create invite. Please try again."
	}
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msg, suffixAddError)
}

func (h *Handler) onList(ctx context.Context, opID string, p commandcmd.Payload) error {
	if h.collaboratorStore == nil || h.inviteStore == nil {
		return h.sendListError(ctx, opID, p)
	}

	collaborators, err := h.collaboratorStore.ListCollaborators(ctx)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to list collaborators")
		return h.sendListError(ctx, opID, p)
	}

	invites, err := h.inviteStore.ListInvites(ctx)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to list invites")
		return h.sendListError(ctx, opID, p)
	}

	var lines []string
	if len(collaborators) > 0 {
		lines = append(lines, "Collaborators:")
		for _, c := range collaborators {
			name := "unknown"
			if strings.TrimSpace(c.Username) != "" {
				if p.Transport == transportTelegram {
					name = "@" + c.Username
				} else {
					name = c.Username
				}
			} else if strings.TrimSpace(c.FirstName) != "" {
				name = c.FirstName
			}
			lines = append(lines, fmt.Sprintf("• %s (%s) - added %s",
				c.UserID, name, c.AddedAt.Format("2006-01-02 15:04")))
		}
	} else {
		lines = append(lines, "No collaborators")
	}

	if len(invites) > 0 {
		lines = append(lines, "", "Active Invites:")
		for _, inv := range invites {
			lines = append(lines, fmt.Sprintf("expires %s", inv.ExpiresAt.Format("2006-01-02 15:04")))
		}
	}

	message := strings.Join(lines, "\n")
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, message, suffixListSuccess)
}

func (h *Handler) sendListError(ctx context.Context, opID string, p commandcmd.Payload) error {
	msg := "Failed to load user list."
	if p.Transport == transportTelegram {
		msg = "Failed to list collaborators. Please try again."
	}
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msg, suffixListError)
}

func (h *Handler) onRemove(ctx context.Context, opID string, p commandcmd.Payload, args []string) error {
	if len(args) < 2 {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Usage: /user remove <user_id>", suffixUsage)
	}

	userID := strings.TrimSpace(args[1])
	if userID == "" {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "User ID required", suffixUsage)
	}

	if h.collaboratorStore == nil {
		return h.sendRemoveError(ctx, opID, p)
	}

	if err := h.collaboratorStore.RemoveCollaborator(ctx, userID); err != nil {
		h.logger.Error().Err(err).Str("target_user_id", userID).Msg("failed to remove collaborator")
		return h.sendRemoveError(ctx, opID, p)
	}

	message := fmt.Sprintf("Collaborator removed: %s", userID)
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, message, suffixRemoveSuccess)
}

func (h *Handler) sendRemoveError(ctx context.Context, opID string, p commandcmd.Payload) error {
	msg := "Could not remove collaborator."
	if p.Transport == transportTelegram {
		msg = "Could not remove collaborator. Please try again."
	}
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msg, suffixRemoveError)
}

func (h *Handler) isOwner(p commandcmd.Payload) bool {
	if p.Access.Owner {
		return true
	}
	if h.ownerStore == nil {
		return false
	}
	if p.Transport == transportTelegram {
		if id, err := strconv.ParseInt(strings.TrimSpace(p.Principal), 10, 64); err == nil {
			return h.ownerStore.IsOwner(id)
		}
	}
	subject := userSubject(p.Transport, p.Principal)
	return h.ownerStore.IsOwnerSubject(subject)
}

func userSubject(transport, principal string) string {
	trimmed := strings.TrimSpace(principal)
	switch transport {
	case transportTelegram:
		if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return auth.TelegramSubject(id)
		}
	case transportZulip:
		if id, err := strconv.Atoi(trimmed); err == nil {
			return auth.ZulipSubject(id)
		}
		if id, err := strconv.Atoi(strings.TrimPrefix(trimmed, "zulip-session-")); err == nil {
			return auth.ZulipSubject(id)
		}
	}
	return transport + ":" + trimmed
}
