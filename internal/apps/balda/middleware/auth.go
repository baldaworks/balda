package middleware

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/auth"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
)

type contextKey string

const ownerKey contextKey = "owner"

// OwnerOnly is a middleware that checks if the user is the registered owner.
// It allows /start command for all users (for authentication).
// For other commands, it checks ownership and rejects unauthorized users.
func OwnerOnly(ownerStore *auth.OwnerStore, tgClient client.ClientWithResponsesInterface) eventemitter.Middleware {
	return &ownerOnlyMiddleware{
		ownerStore: ownerStore,
		tgClient:   tgClient,
	}
}

type ownerOnlyMiddleware struct {
	ownerStore *auth.OwnerStore
	tgClient   client.ClientWithResponsesInterface
}

func (m *ownerOnlyMiddleware) Handle(next eventemitter.Listener) eventemitter.Listener {
	return eventemitter.ListenerFunc(func(ctx context.Context, payload any) error {
		logger := zerolog.Ctx(ctx)

		var userID int64
		var chatID int64
		var isCommand bool
		var command string

		switch e := payload.(type) {
		case *events.CommandEvent:
			if e.Message != nil && e.Message.From != nil {
				userID = e.Message.From.Id
				chatID = e.Message.Chat.Id
				isCommand = true
				command = e.Command
			}
		case *events.MessageEvent:
			if e.Message != nil && e.Message.From != nil {
				userID = e.Message.From.Id
				chatID = e.Message.Chat.Id
			}
		default:
			// For other event types, pass through
			return next.Handle(ctx, payload)
		}

		// Allow /start command for all users (for authentication)
		if isCommand && command == "start" {
			ctx = context.WithValue(ctx, ownerKey, &OwnerInfo{
				UserID: userID,
				ChatID: chatID,
			})
			return next.Handle(ctx, payload)
		}

		// Check if owner is registered
		if !m.ownerStore.HasOwner() {
			logger.Warn().Int64("user_id", userID).Msg("No owner registered, rejecting command")
			if err := m.sendUnauthorizedMessage(ctx, chatID, "No owner registered. Please start the bot with /start first."); err != nil {
				logger.Error().Err(err).Msg("Failed to send unauthorized message")
			}
			return eventemitter.ErrBreak
		}

		// Check if user is the owner
		if !m.ownerStore.IsOwner(userID) {
			logger.Warn().Int64("user_id", userID).Msg("User is not the owner, rejecting command")
			if err := m.sendUnauthorizedMessage(ctx, chatID, "Unauthorized. Only the bot owner can use this command."); err != nil {
				logger.Error().Err(err).Msg("Failed to send unauthorized message")
			}
			return eventemitter.ErrBreak
		}

		// User is owner, add owner info to context
		ctx = context.WithValue(ctx, ownerKey, &OwnerInfo{
			UserID: userID,
			ChatID: chatID,
		})

		return next.Handle(ctx, payload)
	})
}

// OwnerInfo contains information about the owner from the context.
type OwnerInfo struct {
	UserID int64
	ChatID int64
}

func (m *ownerOnlyMiddleware) sendUnauthorizedMessage(ctx context.Context, chatID int64, text string) error {
	_, err := m.tgClient.SendMessageWithResponse(ctx, client.SendMessageJSONRequestBody{
		ChatId: chatID,
		Text:   text,
	})
	if err != nil {
		return fmt.Errorf("sending unauthorized message to chat %d: %w", chatID, err)
	}
	return nil
}
