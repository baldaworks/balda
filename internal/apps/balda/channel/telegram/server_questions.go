package telegram

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/tgbotkit/runtime/events"
)

const (
	questionCallbackSelectedMessage    = "Selected."
	questionCallbackUnavailableMessage = "This choice is not available to you."
)

func (s *Server) HandleQuestionCallback(ctx context.Context, event *events.CallbackQueryEvent) error {
	if s == nil || s.channel == nil {
		return nil
	}
	callback, ok := s.channel.CallbackContextFromEvent(event)
	if !ok {
		if event != nil && event.CallbackQuery != nil {
			_ = s.channel.AnswerQuestionCallback(ctx, event.CallbackQuery.Id, "This choice is no longer available.", true)
		}
		return nil
	}
	if s.questionService == nil {
		return s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "This request is unavailable.", true)
	}
	if s.ownerStore == nil || s.collaboratorStore == nil {
		return s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "This request is unavailable.", true)
	}
	if !s.canAccessCollaboratorScope(ctx, callback.UserID) {
		return s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "You cannot answer this request.", true)
	}
	receivedAt := time.Now()
	if s.now != nil {
		receivedAt = s.now()
	}
	result, err := s.questionService.ResolveSelectionDetailed(ctx, questioncmd.InboundSelection{
		Provider:          string(deliverycmd.ChannelTypeTelegram),
		SessionID:         callback.Locator.SessionID,
		ConversationKey:   callback.Locator.AddressKey,
		QuestionID:        callback.QuestionID,
		ProviderMessageID: callback.ProviderMessageID,
		User:              questioncmd.UserRef{UserID: telegramref.UserID(callback.UserID)},
		OptionIndex:       callback.OptionIndex,
		ReceivedAt:        receivedAt,
	})
	if err != nil {
		_ = s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, "Could not process this choice.", true)
		return err
	}
	message, alert := questionCallbackSelectedMessage, false
	switch {
	case !result.Matched || result.Inactive:
		message = "This request has expired."
	case result.Invalid:
		message, alert = questionCallbackUnavailableMessage, true
	case !result.Settled:
		message = "This request has already been answered."
	}
	ackErr := s.channel.AnswerQuestionCallback(ctx, callback.CallbackQueryID, message, alert)
	if !result.Settled {
		return ackErr
	}
	if dispatchErr := dispatchQuestionContinuation(ctx, s.actorDispatcher, result.Continuation); dispatchErr != nil {
		return dispatchErr
	}
	return ackErr
}

func (s *Server) HandleQuestionReply(ctx context.Context, messageCtx MessageContext) (bool, error) {
	text := messageCtx.Text
	if s == nil || s.questionService == nil || messageCtx.ReplyToMessageID <= 0 || strings.TrimSpace(text) == "" {
		return false, nil
	}
	receivedAt := time.Now()
	if s.now != nil {
		receivedAt = s.now()
	}
	result, err := s.questionService.ResolveReplyDetailed(ctx, questioncmd.InboundReply{
		Provider:         "telegram",
		SessionID:        messageCtx.Locator.SessionID,
		ConversationKey:  messageCtx.Locator.AddressKey,
		ReplyToMessageID: strconv.Itoa(messageCtx.ReplyToMessageID),
		MessageID:        strconv.Itoa(messageCtx.MessageID),
		User:             questioncmd.UserRef{UserID: telegramref.UserID(messageCtx.UserID)},
		Text:             text,
		ReceivedAt:       receivedAt,
	})
	if err != nil || !result.Matched {
		return result.Matched, err
	}
	if !result.Settled {
		return true, nil
	}
	if err := dispatchQuestionContinuation(ctx, s.actorDispatcher, result.Continuation); err != nil {
		return true, err
	}
	return true, nil
}

func (s *Server) canAccessCollaboratorScope(ctx context.Context, userID int64) bool {
	allowed, err := s.accessCollaboratorScope(ctx, userID)
	return err == nil && allowed
}

func (s *Server) accessCollaboratorScope(ctx context.Context, userID int64) (bool, error) {
	if s.ownerStore != nil && s.ownerStore.IsOwner(userID) {
		return true, nil
	}
	if s.collaboratorStore == nil {
		return false, nil
	}
	collaborator, found, err := s.collaboratorStore.GetCollaborator(ctx, fmt.Sprintf("%d", userID))
	if err != nil {
		return false, fmt.Errorf("look up telegram collaborator: %w", err)
	}
	return found && collaborator != nil, nil
}

func dispatchQuestionContinuation(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}
