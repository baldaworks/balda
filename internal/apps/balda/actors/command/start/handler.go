// Package start owns start command policy and onboarding orchestration.
package start

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const (
	transportTelegram = "telegram"
	transportZulip    = "zulip"
	transportSlack    = "slackagent"

	modeOwner        = "owner"
	modeInvite       = "invite"
	modeChannelToken = "channel_token"
)

// OwnerStore defines owner persistence operations required by the start command.
type OwnerStore interface {
	HasOwner() bool
	IsOwner(userID int64) bool
	IsOwnerSubject(subject string) bool
	RegisterOwner(userID, chatID int64) (bool, error)
	RegisterOwnerSubject(subject string) (bool, error)
	BindOwnerTelegram(userID, chatID int64) error
	BindOwnerSubject(subject string) error
}

// InviteStore defines invite lookup operations required by the start command.
type InviteStore interface {
	GetInvite(ctx context.Context, token string) (*auth.Invite, error)
}

// CollaboratorStore defines collaborator persistence operations required by the start command.
type CollaboratorStore interface {
	GetCollaborator(ctx context.Context, userID string) (*auth.Collaborator, bool, error)
	AddCollaborator(ctx context.Context, collaborator auth.Collaborator) error
}

// ChannelAuthService defines transparent channel token operations required by the start command.
type ChannelAuthService interface {
	ConsumeOwnerBind(ctx context.Context, channel, subject, token string) (bool, error)
	CreateMissingOwnerBindTokens(ctx context.Context, createdBy string) ([]auth.OwnerBindToken, error)
}

// OwnerActivator activates or bootstraps the owner session on the transport.
type OwnerActivator interface {
	ActivateOwner(ctx context.Context, locator deliverycmd.Locator, principal string) error
}

// Handler handles the /start command across supported transports.
type Handler struct {
	ownerStore        OwnerStore
	inviteStore       InviteStore
	collaboratorStore CollaboratorStore
	channelAuth       ChannelAuthService
	activator         OwnerActivator
	dispatcher        actortransport.Dispatcher
	authToken         string
	logger            zerolog.Logger
}

// New creates a new start command Handler.
func New(
	ownerStore OwnerStore,
	inviteStore InviteStore,
	collaboratorStore CollaboratorStore,
	channelAuth ChannelAuthService,
	activator OwnerActivator,
	dispatcher actortransport.Dispatcher,
	authToken string,
	logger zerolog.Logger,
) *Handler {
	return &Handler{
		ownerStore:        ownerStore,
		inviteStore:       inviteStore,
		collaboratorStore: collaboratorStore,
		channelAuth:       channelAuth,
		activator:         activator,
		dispatcher:        dispatcher,
		authToken:         strings.TrimSpace(authToken),
		logger:            logger.With().Str("component", "balda.actors.command.start").Logger(),
	}
}

// Name returns "start".
func (h *Handler) Name() string { return "start" }

type startArgs struct {
	mode  string
	token string
}

func parseArgs(raw, transport string) (startArgs, bool) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return startArgs{}, true
	}
	fields := strings.Fields(trimmed)
	if len(fields) != 1 {
		return startArgs{}, false
	}
	assignment := fields[0]
	if strings.HasPrefix(assignment, "?") {
		return startArgs{}, false
	}
	if auth.LooksLikeChannelToken(assignment) {
		return startArgs{mode: modeChannelToken, token: assignment}, true
	}
	if transport == transportTelegram {
		if strings.HasPrefix(assignment, "owner_") {
			val := strings.TrimSpace(strings.TrimPrefix(assignment, "owner_"))
			if val == "" {
				return startArgs{}, false
			}
			return startArgs{mode: modeOwner, token: val}, true
		}
		if strings.HasPrefix(assignment, "invite_") {
			val := strings.TrimSpace(strings.TrimPrefix(assignment, "invite_"))
			if val == "" {
				return startArgs{}, false
			}
			return startArgs{mode: modeInvite, token: val}, true
		}
	}
	if strings.Count(assignment, "=") != 1 {
		return startArgs{}, false
	}
	key, val, _ := strings.Cut(assignment, "=")
	key = strings.TrimSpace(key)
	val = strings.TrimSpace(val)
	if key == "" || val == "" {
		return startArgs{}, false
	}
	switch key {
	case modeOwner, modeInvite:
		return startArgs{mode: key, token: val}, true
	default:
		return startArgs{}, false
	}
}

func invalidFormatMessage(transport string) string {
	if transport == transportTelegram {
		return "Invalid /start format. Use one of:\n• /start owner=<your_owner_token>\n• /start invite=<your_invite_token>\n\nIf using a link, use one of:\n• https://t.me/<bot_username>?start=owner_<your_owner_token>\n• https://t.me/<bot_username>?start=invite_<your_invite_token>"
	}
	return "Invalid /start format. Use one of:\n• /start owner=<your_owner_token>\n• /start invite=<your_invite_token>"
}

func welcomeMessage(transport string) string {
	if transport == transportTelegram {
		return "Welcome to Balda Bot!\n\nTo authenticate, send /start owner=<your_owner_token>"
	}
	return "Welcome to Balda Bot!\n\nTo authenticate:\n• /start owner=<your_owner_token>\n• /start invite=<your_invite_token>"
}

// Handle processes one /start command request.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !p.Conversation.Direct {
		if p.Transport == transportTelegram {
			return nil
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "This command is only available in direct messages.", "start-dm-only")
	}

	args, ok := parseArgs(p.Args, p.Transport)
	if !ok {
		h.logger.Warn().Str("principal", p.Principal).Str("transport", p.Transport).Msg("malformed /start argument")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, invalidFormatMessage(p.Transport), "start-invalid-format")
	}

	if args.mode == modeChannelToken {
		return h.handleChannelToken(ctx, env, p, args.token)
	}

	hasOwner := h.ownerStore != nil && h.ownerStore.HasOwner()
	if hasOwner {
		return h.handleExistingOwnerState(ctx, env, p, args)
	}

	return h.handleUnregisteredState(ctx, env, p, args)
}

func (h *Handler) handleChannelToken(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload, token string) error {
	if h.channelAuth == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Token authentication is unavailable right now.", "start-auth-unavailable")
	}
	subject := userSubject(p.Transport, p.Principal)
	consumed, err := h.channelAuth.ConsumeOwnerBind(ctx, p.Transport, subject, token)
	if err != nil {
		h.logger.Warn().Err(err).Str("principal", p.Principal).Str("transport", p.Transport).Msg("failed to consume owner bind token")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Failed to process token. Please try again.", "start-token-failed")
	}
	if !consumed {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "This token is invalid or has expired.", "start-token-invalid")
	}

	if p.Transport == transportTelegram && h.ownerStore != nil {
		userID, chatID := parseTelegramPrincipal(p)
		if err := h.ownerStore.BindOwnerTelegram(userID, chatID); err != nil {
			h.logger.Warn().Err(err).Int64("user_id", userID).Msg("failed to bind telegram owner")
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Failed to connect Telegram account. Please try again.", "start-bind-failed")
		}
	}

	if h.activator != nil {
		if err := h.activator.ActivateOwner(ctx, p.Locator, p.Principal); err != nil {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Could not start owner session. Please try again.", "start-activate-failed")
		}
	}

	msg := fmt.Sprintf("This %s account is now connected to the Balda owner.", transportDisplayName(p.Transport))
	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, msg, "start-token-connected")
}

func (h *Handler) handleExistingOwnerState(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload, args startArgs) error {
	if args.mode == modeInvite {
		return h.handleInvite(ctx, env, p, args.token)
	}

	if h.isOwner(p) {
		var startErr error
		if p.Transport == transportTelegram && h.ownerStore != nil {
			userID, chatID := parseTelegramPrincipal(p)
			startErr = h.ownerStore.BindOwnerTelegram(userID, chatID)
			if startErr != nil {
				h.logger.Warn().Err(startErr).Msg("failed to update owner chatID")
			}
		}
		if startErr == nil && h.activator != nil {
			startErr = h.activator.ActivateOwner(ctx, p.Locator, p.Principal)
		}
		msg := "You are already registered as the bot owner."
		subject := userSubject(p.Transport, p.Principal)
		if bundle, ok := ownerBindBundle(ctx, h.channelAuth, subject); ok {
			msg += "\n\n" + bundle
		}
		if startErr != nil {
			msg += "\n\nCould not start owner session. Please try again."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, msg, "start-owner-exists")
	}

	if h.collaboratorStore != nil {
		if _, ok, err := h.collaboratorStore.GetCollaborator(ctx, p.Principal); err != nil {
			h.logger.Warn().Err(err).Str("principal", p.Principal).Msg("failed to check collaborator during /start")
		} else if ok {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "You are already a bot collaborator.", "start-already-collaborator")
		}
	}

	if p.Transport == transportTelegram {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Bot owner is already registered. Only the owner can use this bot.", "start-owner-registered")
	}
	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Bot owner is already registered.", "start-owner-registered")
}

func (h *Handler) handleUnregisteredState(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload, args startArgs) error {
	if args.mode == "" {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, welcomeMessage(p.Transport), "start-welcome")
	}

	if args.mode == modeInvite {
		return h.handleInvite(ctx, env, p, args.token)
	}

	if h.ownerStore == nil {
		h.logger.Error().Msg("owner store is unavailable")
		errMsg := "Failed to register owner. Please try again."
		if p.Transport == transportZulip {
			errMsg = "Could not register owner. Ask the operator to check Balda storage configuration."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "start-owner-store-unavailable")
	}

	if h.authToken == "" || args.token != h.authToken {
		h.logger.Warn().Str("principal", p.Principal).Str("transport", p.Transport).Msg("invalid auth token provided")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Invalid authentication token. Please try again.", "start-auth-invalid")
	}

	var registered bool
	var err error
	if p.Transport == transportTelegram {
		userID, chatID := parseTelegramPrincipal(p)
		registered, err = h.ownerStore.RegisterOwner(userID, chatID)
	} else {
		subject := userSubject(p.Transport, p.Principal)
		registered, err = h.ownerStore.RegisterOwnerSubject(subject)
	}
	if err != nil {
		h.logger.Error().Err(err).Str("principal", p.Principal).Msg("failed to register owner")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Failed to register owner. Please try again.", "start-register-failed")
	}
	if !registered {
		errMsg := "Bot owner is already registered."
		if p.Transport == transportTelegram {
			errMsg = "Owner is already registered."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "start-already-registered")
	}

	var activateErr error
	if h.activator != nil {
		activateErr = h.activator.ActivateOwner(ctx, p.Locator, p.Principal)
	}

	if p.Transport == transportTelegram {
		text := "Congratulations, Owner! You are now registered as the bot owner."
		if activateErr != nil {
			text += "\n\nCould not start owner session. Please try again."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, text, "start-registered")
	}

	text := "You are now registered as the bot owner."
	subject := userSubject(p.Transport, p.Principal)
	if bundle, ok := ownerBindBundle(ctx, h.channelAuth, subject); ok {
		text += "\n\n" + bundle
	}
	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, text, "start-registered")
}

func (h *Handler) handleInvite(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload, token string) error {
	if h.isOwner(p) {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "You are already the bot owner.", "start-already-owner")
	}

	if h.collaboratorStore != nil {
		if _, ok, err := h.collaboratorStore.GetCollaborator(ctx, p.Principal); err != nil {
			h.logger.Warn().Err(err).Str("principal", p.Principal).Msg("failed to check collaborator during invite")
		} else if ok {
			return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "You are already a collaborator.", "start-already-collaborator")
		}
	}

	if h.inviteStore == nil || h.collaboratorStore == nil {
		h.logger.Error().Msg("invite or collaborator store is unavailable")
		errMsg := "Failed to process invite. Please try again."
		if p.Transport == transportZulip {
			errMsg = "Failed to process invite. Ask the operator to check Balda storage configuration."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "start-invite-store-unavailable")
	}

	invite, err := h.inviteStore.GetInvite(ctx, token)
	if err != nil {
		h.logger.Warn().Err(err).Str("token", token).Msg("failed to get invite")
		errMsg := "Failed to process invite. Please try again."
		if p.Transport == transportZulip {
			errMsg = "Failed to process invite. Ask the operator to check Balda storage configuration."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "start-invite-failed")
	}
	if invite == nil {
		errMsg := "This invite token is invalid or has expired."
		if p.Transport == transportTelegram {
			errMsg = "This invite link is invalid or has expired."
		}
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, errMsg, "start-invite-invalid")
	}

	collaborator := auth.Collaborator{
		UserID:  p.Principal,
		AddedBy: invite.CreatedBy,
		AddedAt: time.Now(),
	}
	if err := h.collaboratorStore.AddCollaborator(ctx, collaborator); err != nil {
		h.logger.Error().Err(err).Str("principal", p.Principal).Msg("failed to add collaborator from invite")
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Failed to complete registration. Please try again.", "start-invite-add-failed")
	}

	h.logger.Info().Str("principal", p.Principal).Str("invited_by", invite.CreatedBy).Msg("collaborator registered via invite")
	return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, "Welcome! You are now a bot collaborator.", "start-invite-success")
}

func (h *Handler) isOwner(p commandcmd.Payload) bool {
	if h.ownerStore == nil {
		return false
	}
	if p.Access.Owner {
		return true
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
	}
	return transport + ":" + trimmed
}

func parseTelegramPrincipal(p commandcmd.Payload) (int64, int64) {
	userID, _ := strconv.ParseInt(strings.TrimSpace(p.Principal), 10, 64)
	address, ok, err := telegramref.DecodeLocator(p.Locator)
	if err == nil && ok {
		return userID, address.ChatID
	}
	return userID, 0
}

func transportDisplayName(transport string) string {
	switch strings.ToLower(strings.TrimSpace(transport)) {
	case transportTelegram:
		return "Telegram"
	case transportZulip:
		return "Zulip"
	case transportSlack:
		return "Slack"
	default:
		return transport
	}
}

func ownerBindBundle(ctx context.Context, authService ChannelAuthService, createdBy string) (string, bool) {
	if authService == nil {
		return "", false
	}
	tokens, err := authService.CreateMissingOwnerBindTokens(ctx, createdBy)
	if err != nil || len(tokens) == 0 {
		return "", false
	}
	lines := []string{"Connect your other Balda channels:"}
	for _, token := range tokens {
		switch token.Channel {
		case auth.ChannelTelegram:
			lines = append(lines, "", "Telegram:", fmt.Sprintf("DM Balda this command: /start %s", token.Token))
		case auth.ChannelSlack:
			lines = append(lines, "", "Slack:", "DM Balda this token:", token.Token)
		case auth.ChannelZulip:
			lines = append(lines, "", "Zulip:", "DM Balda this token:", token.Token)
		}
	}
	return strings.Join(lines, "\n"), true
}
