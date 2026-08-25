package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/ingressapp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/welcome"
	"github.com/rs/zerolog/log"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
)

const (
	ownerSessionLabel = "balda"
	autoSessionLabel  = "auto"

	telegramIngressReasonProviderUnavailable = "provider_unavailable"
	telegramIngressReasonSessionUnavailable  = "session_unavailable"
)

func (s *Server) Register(registry Registry) {
	registry.OnMessage(s.HandleMessage)
	registry.OnCallbackDataPrefix(QuestionCallbackPrefix, s.HandleQuestionCallback)
	registry.OnMessageType(messagetype.ForumTopicCreated, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicEdited, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicClosed, s.onForumTopicLifecycle)
	registry.OnMessageType(messagetype.ForumTopicReopened, s.onForumTopicLifecycle)
}

func (s *Server) HandleMessage(ctx context.Context, event *events.MessageEvent) error {
	messageCtx, ok := s.channel.MessageContextFromEvent(event)
	if !ok {
		return nil
	}
	s.logger.Info().
		Str("transport", "telegram").
		Str("message_type", string(event.Type)).
		Bool("media_group", strings.TrimSpace(messageCtx.MediaGroupID) != "").
		Int("attachments_count", len(messageCtx.Attachments)).
		Msg("received inbound telegram transport message")

	if s.getOwnerID() == 0 {
		return nil
	}
	allowed, err := s.accessCollaboratorScope(ctx, messageCtx.UserID)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	if s.getChatID() == 0 {
		s.setChatID(messageCtx.ChatID)
		log.Info().Int64("chat_id", messageCtx.ChatID).Msg("Chat ID set from message")
	}
	if messageCtx.HasCommand {
		return nil
	}
	if s.channel.CollectMediaGroup(messageCtx, func(groupCtx context.Context, grouped MessageContext) {
		if err := s.handleAcceptedMessage(groupCtx, grouped); err != nil {
			s.logger.Error().Err(err).
				Str("session_id", grouped.Locator.SessionID).
				Str("media_group_id", grouped.MediaGroupID).
				Msg("failed to handle inbound telegram media group")
		}
	}) {
		return nil
	}
	return s.handleAcceptedMessage(ctx, messageCtx)
}

func (s *Server) handleAcceptedMessage(ctx context.Context, messageCtx MessageContext) error {
	if s.attachmentStore != nil && len(messageCtx.Attachments) > 0 {
		persisted, err := s.attachmentStore.PersistTelegram(ctx, messageCtx.Attachments)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to persist inbound telegram attachments")
		} else {
			messageCtx.Attachments = persisted
		}
	}
	if handled, err := s.HandleQuestionReply(ctx, messageCtx); err != nil {
		s.logger.Warn().Err(err).Str("session_id", messageCtx.Locator.SessionID).Msg("failed to handle question reply")
		_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, messageCtx.Locator, "Could not process this reply right now. Please try again.")
		return nil
	} else if handled {
		return nil
	}

	var text string
	if messageCtx.IsDM {
		text = NormalizeDMText(messageCtx)
	} else {
		botUserID, botUsername := s.getBotIdentity()
		normalized, ok := NormalizePublicText(messageCtx, botUserID, botUsername)
		if !ok {
			return nil
		}
		text = normalized
	}
	if strings.TrimSpace(text) == "" && len(messageCtx.Attachments) == 0 {
		return nil
	}
	nowFn := time.Now
	if s.now != nil {
		nowFn = s.now
	}
	inbound := NormalizeInbound(messageCtx, appendAttachmentSummary(text, messageCtx.Attachments), nowFn())
	service, err := s.telegramIngressService()
	if err != nil {
		return err
	}
	result, err := service.Process(ctx, inbound)
	if err != nil && result.Settlement.Outcome == turncmd.InboundRetry {
		if baldaexecution.IsCommandQueueFull(err) {
			_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, messageCtx.Locator, "Session command queue is full. Please wait or use /cancel.")
		} else {
			s.logger.Error().Err(err).
				Str("session_id", messageCtx.Locator.SessionID).
				Str("inbound_id", string(result.InboundID)).
				Msg("failed to durably accept inbound telegram message")
			_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, messageCtx.Locator, "Failed to publish your message for processing. Please try again.")
		}
		return err
	}
	if err != nil {
		s.logger.Warn().Err(err).
			Str("session_id", messageCtx.Locator.SessionID).
			Str("inbound_id", string(result.InboundID)).
			Str("settlement", string(result.Settlement.Outcome)).
			Msg("terminal inbound telegram message failure")
	}
	return nil
}

func (s *Server) onForumTopicLifecycle(ctx context.Context, event *events.MessageEvent) error {
	lifecycle, ok := s.channel.TopicLifecycleFromEvent(event)
	if !ok {
		return nil
	}
	chatID := lifecycle.ChatID
	boundChatID := s.getChatID()
	if boundChatID != 0 && chatID != boundChatID {
		return nil
	}
	topicID := lifecycle.TopicID
	if topicID <= 0 {
		s.logger.Debug().Int64("chat_id", chatID).Str("event_type", string(lifecycle.Type)).Msg("ignoring forum topic lifecycle event without topic id")
		return nil
	}
	evt := s.logger.Info().Int64("chat_id", chatID).Int("topic_id", topicID).Int("message_id", lifecycle.MessageID).Str("event_type", string(lifecycle.Type))
	if lifecycle.UserID != 0 {
		evt = evt.Int64("user_id", lifecycle.UserID)
	}
	switch lifecycle.Type {
	case messagetype.ForumTopicCreated:
		evt.Msg("forum topic created")
	case messagetype.ForumTopicEdited:
		evt.Msg("forum topic edited")
	case messagetype.ForumTopicClosed:
		evt.Msg("forum topic closed")
		if err := submitSessionCancelControl(ctx, s.actorDispatcher, lifecycle.Locator, "system", "session canceled because forum topic was closed", false); err != nil {
			s.logger.Warn().Err(err).Int64("chat_id", chatID).Int("topic_id", topicID).Msg("failed to publish forum-topic-close cancel control command")
		}
		if s.sessionManager != nil {
			s.sessionManager.StopSession(lifecycle.Locator)
		}
	case messagetype.ForumTopicReopened:
		evt.Msg("forum topic reopened")
	default:
		evt.Msg("forum topic lifecycle event")
	}
	return nil
}

func (s *Server) telegramIngressService() (*ingressapp.Service, error) {
	if s == nil {
		return nil, fmt.Errorf("telegram server is required")
	}
	return ingressapp.NewWithLogger(
		ingressapp.AuthorizerFunc(s.authorizeTelegramInbound),
		ingressapp.SessionPreparerFunc(s.prepareTelegramSession),
		ingressapp.DispatcherFunc(s.dispatchTelegramInbound),
		s.logger,
	)
}

func (s *Server) authorizeTelegramInbound(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.Authorization, error) {
	userID, err := telegramref.ParseUserID(inbound.UserID)
	if err != nil {
		return ingressapp.Authorization{Reason: ingressapp.ReasonUnauthorized}, nil
	}
	allowed, err := s.accessCollaboratorScope(ctx, userID)
	if err != nil {
		return ingressapp.Authorization{}, err
	}
	return ingressapp.Authorization{Allowed: allowed, Reason: ingressapp.ReasonUnauthorized}, nil
}

func (s *Server) prepareTelegramSession(ctx context.Context, inbound ingressapp.InboundContext) (ingressapp.SessionPreparation, error) {
	locator := inboundLocator(inbound)
	transportUserID := strings.TrimSpace(inbound.UserID)
	var ts *baldasession.TopicSession
	var err error
	if inbound.Direct && inbound.TopicID == 0 {
		existingSession, _ := s.sessionManager.GetSession(locator)
		sendOwnerWelcome := existingSession == nil
		baldaProviderName := s.getProviderName()
		if baldaProviderName == "" {
			_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Balda is not ready right now. Please close this chat and try again.")
			return rejectedSession(telegramIngressReasonProviderUnavailable), nil
		}
		ts, err = s.sessionManager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, ownerSessionLabel)
		if err != nil {
			s.logger.Error().Err(err).Str("agent", baldaProviderName).Msg("failed to ensure main dm session")
			_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Could not start this session. Please close this chat and try again.")
			return rejectedSession(telegramIngressReasonSessionUnavailable), nil
		}
		if sendOwnerWelcome {
			metadata := s.sessionManager.GetAgentMetadata(baldaProviderName)
			welcomeMsg := welcome.BuildAgentWelcomeMessage(ownerSessionLabel, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
			_ = sendMarkdown(ctx, s.actorDispatcher, serverActorAddress, locator, welcomeMsg)
			s.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
		}
	} else {
		ts, err = s.sessionManager.GetSession(locator)
		if err != nil {
			_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Restoring agent session...")
			ts, err = s.sessionManager.RestoreSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID, AllowBaldaProviderFallback: false})
			if err != nil {
				if errors.Is(err, baldasession.ErrNoPersistedSession) {
					baldaProviderName := s.getProviderName()
					if baldaProviderName == "" {
						_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Balda is not ready right now. Please close this chat topic and try again.")
						return rejectedSession(telegramIngressReasonProviderUnavailable), nil
					}
					ts, err = s.sessionManager.EnsureSession(ctx, baldasession.SessionContext{Locator: locator, UserID: transportUserID}, autoSessionLabel)
					if err != nil {
						s.logger.Error().Err(err).Str("agent", baldaProviderName).Int("topic_id", inbound.TopicID).Msg("failed to create session")
						_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Could not start this session. Please close this chat topic and create a new one.")
						return rejectedSession(telegramIngressReasonSessionUnavailable), nil
					}
				} else {
					s.logger.Warn().Err(err).Int("topic_id", inbound.TopicID).Msg("failed to restore session")
					_ = sendPlain(ctx, s.actorDispatcher, serverActorAddress, locator, "Could not restore this session. Please close this chat topic and create a new one.")
					return rejectedSession(telegramIngressReasonSessionUnavailable), nil
				}
			}
			if ts != nil {
				baldaProviderID := s.getProviderName()
				metadata := s.sessionManager.GetAgentMetadata(baldaProviderID)
				welcomeName := ownerSessionLabel
				if inbound.Direct {
					welcomeName = ts.GetAgentName()
				}
				welcomeMsg := welcome.BuildAgentWelcomeMessage(welcomeName, ts.GetSessionID(), metadata.Type, metadata.Model, metadata.MCPServers)
				_ = sendMarkdown(ctx, s.actorDispatcher, serverActorAddress, locator, welcomeMsg)
				s.sendSessionStartupNotice(ctx, locator, ts.GetSessionID())
			}
		}
	}
	if ts == nil {
		return rejectedSession(telegramIngressReasonSessionUnavailable), nil
	}
	return ingressapp.SessionPreparation{Ready: true, UserID: ts.GetUserID(), RequesterUserID: transportUserID, AgentSessionID: ts.GetAgentSessionID(), TopicID: inbound.TopicID}, nil
}

func (s *Server) dispatchTelegramInbound(ctx context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if s.actorDispatcher == nil {
		return nil, actorlayer.TransientError(errors.New("telegram ingress dispatcher is unavailable"))
	}
	receipt, err := s.actorDispatcher.Dispatch(ctx, envelope)
	if err == nil {
		return receipt, nil
	}
	if baldaexecution.IsCommandQueueFull(err) {
		return nil, actorlayer.TransientError(err)
	}
	return receipt, err
}

func inboundLocator(inbound ingressapp.InboundContext) baldasession.SessionLocator {
	return baldasession.SessionLocator{
		ChannelType: inbound.ChannelType,
		AddressKey:  inbound.AddressKey,
		AddressJSON: inbound.AddressJSON,
		SessionID:   inbound.SessionID,
	}
}

func rejectedSession(reason string) ingressapp.SessionPreparation {
	return ingressapp.SessionPreparation{Reason: reason}
}

func appendAttachmentSummary(text string, attachments []attachment.Descriptor) string {
	attachments = attachment.NormalizeList(attachments)
	if len(attachments) == 0 {
		return text
	}
	var b strings.Builder
	if trimmed := strings.TrimSpace(text); trimmed != "" {
		b.WriteString(trimmed)
		b.WriteString("\n\n")
	}
	b.WriteString("Attachment manifest:\n")
	for i, item := range attachments {
		b.WriteString("- attachment_")
		b.WriteString(strconv.Itoa(i + 1))
		b.WriteString(":\n")
		b.WriteString("  kind: ")
		b.WriteString(string(item.Kind))
		b.WriteString("\n")
		if item.FileName != "" {
			b.WriteString("  file_name: ")
			b.WriteString(item.FileName)
			b.WriteString("\n")
		}
		if item.MIMEType != "" {
			b.WriteString("  mime_type: ")
			b.WriteString(item.MIMEType)
			b.WriteString("\n")
		}
		if item.SizeBytes > 0 {
			b.WriteString("  size_bytes: ")
			b.WriteString(strconv.FormatInt(item.SizeBytes, 10))
			b.WriteString("\n")
		}
		if item.Caption != "" {
			b.WriteString("  caption: ")
			b.WriteString(item.Caption)
			b.WriteString("\n")
		}
		if item.Blob != nil {
			if item.Blob.Store != "" {
				b.WriteString("  blob_store: ")
				b.WriteString(item.Blob.Store)
				b.WriteString("\n")
			}
			if item.Blob.Path != "" {
				b.WriteString("  local_path: ")
				b.WriteString(item.Blob.Path)
				b.WriteString("\n")
			}
			if item.Blob.Key != "" {
				b.WriteString("  blob_key: ")
				b.WriteString(item.Blob.Key)
				b.WriteString("\n")
			}
			if item.Blob.SHA256 != "" {
				b.WriteString("  sha256: ")
				b.WriteString(item.Blob.SHA256)
				b.WriteString("\n")
			}
		}
	}
	return strings.TrimSpace(b.String())
}
