package handlers

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/auth"
	"github.com/normahq/balda/internal/apps/balda/messenger"
	"github.com/normahq/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"go.uber.org/fx"
)

// StartHandler handles /start command for owner authentication and invite consumption.
type StartHandler struct {
	ownerStore        *auth.OwnerStore
	inviteStore       *auth.InviteStore
	collaboratorStore *auth.CollaboratorStore
	messenger         *messenger.Messenger
	authToken         string
	baldaHandler      baldaOwnerActivator
}

type baldaOwnerActivator interface {
	activateOwner(ctx context.Context, ownerID, chatID int64) error
}

type startHandlerParams struct {
	fx.In

	OwnerStore        *auth.OwnerStore
	InviteStore       *auth.InviteStore
	CollaboratorStore *auth.CollaboratorStore
	Messenger         *messenger.Messenger
	AuthToken         string `name:"balda_auth_token"`
}

const (
	startModeOwner  = "owner"
	startModeInvite = "invite"
)

type startCommandArgs struct {
	mode  string
	token string
}

// Register registers the handler with the registry.
func (h *StartHandler) Register(registry tgbotkit.Registry) {
	registry.OnCommand(h.onCommand)
}

func (h *StartHandler) onCommand(ctx context.Context, event *events.CommandEvent) error {
	if event.Command != "start" {
		return nil
	}

	if event.Message.Chat.Type != "private" {
		return nil
	}

	chatID := event.Message.Chat.Id
	userIDStr := fmt.Sprintf("%d", event.Message.From.Id)
	userID := event.Message.From.Id

	log.Debug().
		Int64("user_id", userID).
		Int64("chat_id", chatID).
		Msg("Start command received")

	trimmedArgs := strings.TrimSpace(event.Args)
	args := startCommandArgs{}
	malformed := false
	if trimmedArgs != "" {
		fields := strings.Fields(trimmedArgs)
		if len(fields) != 1 {
			malformed = true
		} else {
			assignment := fields[0]
			switch {
			case strings.HasPrefix(assignment, "?"):
				malformed = true
			case strings.HasPrefix(assignment, startModeOwner+"_"):
				value := strings.TrimSpace(strings.TrimPrefix(assignment, startModeOwner+"_"))
				if value == "" {
					malformed = true
				} else {
					args = startCommandArgs{mode: startModeOwner, token: value}
				}
			case strings.HasPrefix(assignment, startModeInvite+"_"):
				value := strings.TrimSpace(strings.TrimPrefix(assignment, startModeInvite+"_"))
				if value == "" {
					malformed = true
				} else {
					args = startCommandArgs{mode: startModeInvite, token: value}
				}
			default:
				if strings.Count(assignment, "=") != 1 {
					malformed = true
					break
				}
				key, value, _ := strings.Cut(assignment, "=")
				key = strings.TrimSpace(key)
				value = strings.TrimSpace(value)
				if key == "" || value == "" {
					malformed = true
					break
				}
				switch key {
				case startModeOwner, startModeInvite:
					args = startCommandArgs{mode: key, token: value}
				default:
					malformed = true
				}
			}
		}
	}

	if malformed {
		log.Warn().
			Int64("user_id", userID).
			Int64("chat_id", chatID).
			Msg("Malformed /start argument")
		if err := h.messenger.SendPlain(ctx, chatID, "Invalid /start format. Use one of:\n• /start owner=<your_owner_token>\n• /start invite=<your_invite_token>\n\nIf using a link, use one of:\n• https://t.me/<bot_username>?start=owner_<your_owner_token>\n• https://t.me/<bot_username>?start=invite_<your_invite_token>", 0); err != nil {
			return err
		}
		return nil
	}

	if h.ownerStore.HasOwner() {
		if args.mode == startModeInvite {
			return h.handleInviteStart(ctx, chatID, userID, userIDStr, args.token, event.Message.From)
		}
		if h.ownerStore.IsOwner(userID) {
			// Persist chatID for existing owner
			if err := h.ownerStore.UpdateChatID(chatID); err != nil {
				log.Warn().Err(err).Msg("failed to update owner chatID")
			}
			startErr := h.activateBalda(ctx, userID, chatID)
			if startErr == nil {
				log.Info().Int64("user_id", userID).Msg("balda re-activated for existing owner")
			}
			msg := "You are already registered as the bot owner."
			if startErr != nil {
				msg += "\n\nCould not start owner session. Please try again."
			}
			if err := h.messenger.SendPlain(ctx, chatID, msg, 0); err != nil {
				return err
			}
			return nil
		}
		if h.collaboratorStore != nil {
			if _, ok, err := h.collaboratorStore.GetCollaborator(ctx, userIDStr); err != nil {
				log.Warn().Err(err).Str("user_id", userIDStr).Msg("failed to check collaborator during /start")
			} else if ok {
				if err := h.messenger.SendPlain(ctx, chatID, "You are already a bot collaborator.", 0); err != nil {
					return err
				}
				return nil
			}
		}
		if err := h.messenger.SendPlain(ctx, chatID, "Bot owner is already registered. Only the owner can use this bot.", 0); err != nil {
			return err
		}
		return nil
	}

	if args.mode == "" {
		if err := h.messenger.SendPlain(ctx, chatID, "Welcome to Balda Bot!\n\nTo authenticate, send /start owner=<your_owner_token>", 0); err != nil {
			return err
		}
		return nil
	}

	if args.mode == startModeInvite {
		return h.handleInviteStart(ctx, chatID, userID, userIDStr, args.token, event.Message.From)
	}

	if args.token != h.authToken {
		log.Warn().
			Int64("user_id", userID).
			Int64("chat_id", chatID).
			Msg("Invalid auth token provided")
		if err := h.messenger.SendPlain(ctx, chatID, "Invalid authentication token. Please try again.", 0); err != nil {
			return err
		}
		return nil
	}

	username := ""
	if event.Message.From.Username != nil {
		username = *event.Message.From.Username
	}
	lastName := ""
	if event.Message.From.LastName != nil {
		lastName = *event.Message.From.LastName
	}

	registered, err := h.ownerStore.RegisterOwner(userID, chatID, username, event.Message.From.FirstName, lastName)
	if err != nil {
		log.Error().Err(err).Int64("user_id", userID).Msg("Failed to register owner")
		if sendErr := h.messenger.SendPlain(ctx, chatID, "Failed to register owner. Please try again.", 0); sendErr != nil {
			return sendErr
		}
		return nil
	}

	if !registered {
		if err := h.messenger.SendPlain(ctx, chatID, "Owner is already registered.", 0); err != nil {
			return err
		}
		return nil
	}

	log.Info().
		Int64("user_id", userID).
		Str("username", username).
		Msg("Owner registered successfully")

	startErr := h.activateBalda(ctx, userID, chatID)
	name := event.Message.From.FirstName
	if name == "" {
		name = "Owner"
	}

	text := fmt.Sprintf("Congratulations, %s! You are now registered as the bot owner.", name)
	if startErr != nil {
		text += "\n\nCould not start owner session. Please try again."
	}
	if err := h.messenger.SendPlain(ctx, chatID, text, 0); err != nil {
		return err
	}
	return nil
}

func (h *StartHandler) activateBalda(ctx context.Context, ownerID, chatID int64) error {
	if h.baldaHandler == nil {
		log.Warn().Msg("balda handler is nil; skipping owner session activation")
		return nil
	}
	if err := h.baldaHandler.activateOwner(ctx, ownerID, chatID); err != nil {
		log.Warn().
			Err(err).
			Int64("owner_id", ownerID).
			Int64("chat_id", chatID).
			Msg("failed to start owner session during /start")
		return err
	}
	return nil
}

func (h *StartHandler) handleInviteStart(ctx context.Context, chatID, userID int64, userIDStr, token string, from *client.User) error {
	if h.ownerStore.IsOwner(userID) {
		if err := h.messenger.SendPlain(ctx, chatID, "You are already the bot owner.", 0); err != nil {
			return err
		}
		return nil
	}
	if h.collaboratorStore != nil {
		if _, ok, err := h.collaboratorStore.GetCollaborator(ctx, userIDStr); err != nil {
			log.Warn().Err(err).Str("user_id", userIDStr).Msg("failed to check collaborator")
		} else if ok {
			if err := h.messenger.SendPlain(ctx, chatID, "You are already a collaborator.", 0); err != nil {
				return err
			}
			return nil
		}
	}
	if h.inviteStore == nil || h.collaboratorStore == nil {
		log.Error().Msg("invite or collaborator store is nil during /start invite flow")
		if err := h.messenger.SendPlain(ctx, chatID, "Failed to process invite. Please try again.", 0); err != nil {
			return err
		}
		return nil
	}

	invite, err := h.inviteStore.GetInvite(ctx, token)
	if err != nil {
		log.Warn().Err(err).Str("token", token).Msg("failed to get invite")
		if err := h.messenger.SendPlain(ctx, chatID, "Failed to process invite. Please try again.", 0); err != nil {
			return err
		}
		return nil
	}
	if invite == nil {
		if err := h.messenger.SendPlain(ctx, chatID, "This invite link is invalid or has expired.", 0); err != nil {
			return err
		}
		return nil
	}

	collaborator := auth.Collaborator{
		UserID:  userIDStr,
		AddedBy: invite.CreatedBy,
		AddedAt: time.Now(),
	}
	if from.Username != nil {
		collaborator.Username = *from.Username
	}
	collaborator.FirstName = from.FirstName
	if err := h.collaboratorStore.AddCollaborator(ctx, collaborator); err != nil {
		log.Error().Err(err).Msg("failed to add collaborator from invite")
		if err := h.messenger.SendPlain(ctx, chatID, "Failed to complete registration. Please try again.", 0); err != nil {
			return err
		}
		return nil
	}

	log.Info().Str("user_id", userIDStr).Str("invited_by", invite.CreatedBy).Msg("user registered as collaborator via invite")
	if err := h.messenger.SendPlain(ctx, chatID, "Welcome! You are now a bot collaborator.", 0); err != nil {
		return err
	}
	return nil
}
