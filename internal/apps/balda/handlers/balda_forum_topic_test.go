package handlers

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/messenger"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
	rtHandlers "github.com/tgbotkit/runtime/handlers"
	"github.com/tgbotkit/runtime/messagetype"
)

var _ tgbotkit.Registry = (*fakeBaldaRegistry)(nil)

const testTelegramMediaGroupID = "album-42"

type fakeBaldaRegistry struct {
	onMessageCalls   int
	callbackCalls    int
	messageTypeCalls []messagetype.MessageType
}

type testBaldaRegistry struct {
	registry *fakeBaldaRegistry
}

func (r testBaldaRegistry) OnCommand(handler func(context.Context, *events.CommandEvent) error) {
	r.registry.OnCommand(rtHandlers.CommandHandler(handler))
}

func (r testBaldaRegistry) OnMessage(handler func(context.Context, *events.MessageEvent) error) {
	r.registry.OnMessage(rtHandlers.MessageHandler(handler))
}

func (r testBaldaRegistry) OnMessageType(messageType messagetype.MessageType, handler func(context.Context, *events.MessageEvent) error) {
	r.registry.OnMessageType(messageType, rtHandlers.MessageHandler(handler))
}

func (r testBaldaRegistry) OnCallbackDataPrefix(prefix string, handler func(context.Context, *events.CallbackQueryEvent) error) {
	r.registry.OnCallbackDataPrefix(prefix, rtHandlers.CallbackQueryHandler(handler))
}

func (f *fakeBaldaRegistry) OnUpdate(rtHandlers.UpdateHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

func (f *fakeBaldaRegistry) OnMessage(rtHandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	f.onMessageCalls++
	return func() {}
}

func (f *fakeBaldaRegistry) OnMessageType(t messagetype.MessageType, _ rtHandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	f.messageTypeCalls = append(f.messageTypeCalls, t)
	return func() {}
}

func (f *fakeBaldaRegistry) OnCallbackDataPrefix(string, rtHandlers.CallbackQueryHandler) eventemitter.UnsubscribeFunc {
	f.callbackCalls++
	return func() {}
}

func (f *fakeBaldaRegistry) OnCommand(rtHandlers.CommandHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

func TestBaldaHandlerRegister_RegistersForumTopicMessageTypes(t *testing.T) {
	registry := &fakeBaldaRegistry{}
	handler := &BaldaHandler{logger: zerolog.Nop(), channel: newBaldaTestTelegramAdapter()}

	handler.Register(testBaldaRegistry{registry})

	if registry.onMessageCalls != 1 {
		t.Fatalf("OnMessage calls = %d, want 1", registry.onMessageCalls)
	}
	if registry.callbackCalls != 1 {
		t.Fatalf("OnCallbackDataPrefix calls = %d, want 1", registry.callbackCalls)
	}

	want := []messagetype.MessageType{
		messagetype.ForumTopicCreated,
		messagetype.ForumTopicEdited,
		messagetype.ForumTopicClosed,
		messagetype.ForumTopicReopened,
	}
	if len(registry.messageTypeCalls) != len(want) {
		t.Fatalf("OnMessageType calls = %d, want %d", len(registry.messageTypeCalls), len(want))
	}
	for i := range want {
		if registry.messageTypeCalls[i] != want[i] {
			t.Fatalf("OnMessageType[%d] = %q, want %q", i, registry.messageTypeCalls[i], want[i])
		}
	}
}

func TestBaldaHandlerOnForumTopicLifecycle_NonClosingEventsDoNotStopSession(t *testing.T) {
	handler := &BaldaHandler{logger: zerolog.Nop(), channel: newBaldaTestTelegramAdapter()}

	tests := []messagetype.MessageType{
		messagetype.ForumTopicCreated,
		messagetype.ForumTopicEdited,
		messagetype.ForumTopicReopened,
	}

	for _, messageType := range tests {
		t.Run(string(messageType), func(t *testing.T) {
			topicID := 77
			userID := int64(101)
			event := &events.MessageEvent{
				Type: messageType,
				Message: &client.Message{
					MessageId:       42,
					MessageThreadId: &topicID,
					Chat: client.Chat{
						Id:   9001,
						Type: "supergroup",
					},
					From: &client.User{Id: userID},
				},
			}

			if err := handler.onForumTopicLifecycle(context.Background(), event); err != nil {
				t.Fatalf("onForumTopicLifecycle() error = %v", err)
			}
		})
	}
}

func TestBaldaHandlerOnForumTopicLifecycle_ClosedStopsTopicSession(t *testing.T) {
	topicID := 77
	isTopicMessage := true
	locator := baldatelegram.NewLocator(9001, topicID)
	sessionManager := newBaldaSessionManagerWithSession(t, locator, newBaldaTopicSession(t, locator.SessionID))
	turnDispatcher := &fakeTurnDispatcher{}
	handler := &BaldaHandler{
		logger:          zerolog.Nop(),
		channel:         newBaldaTestTelegramAdapter(),
		sessionManager:  sessionManager,
		actorDispatcher: turnDispatcher,
	}
	handler.setChatID(9001)

	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId:       42,
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}

	if len(turnDispatcher.cancelCalls) != 0 {
		t.Fatalf("CancelSession calls = %d, want 0 before control actor runs", len(turnDispatcher.cancelCalls))
	}
	if len(turnDispatcher.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turnDispatcher.commands))
	}
	if turnDispatcher.commands[0].Namespace != baldaexecution.NamespaceJobControl || turnDispatcher.commands[0].Kind != baldaexecution.KindCancel {
		t.Fatalf("published command = %+v, want job control cancel", turnDispatcher.commands[0])
	}
	if _, err := sessionManager.GetSession(locator); err == nil {
		t.Fatal("GetSession() error = nil, want stopped session")
	}
}

func TestBaldaHandlerOnForumTopicLifecycle_IgnoresOtherChatWhenBound(t *testing.T) {
	handler := &BaldaHandler{logger: zerolog.Nop(), channel: newBaldaTestTelegramAdapter()}
	handler.setChatID(9001)

	topicID := 13
	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId:       55,
			MessageThreadId: &topicID,
			Chat: client.Chat{
				Id:   9999,
				Type: "supergroup",
			},
		},
	}

	if err := handler.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}

	if got := handler.getChatID(); got != 9001 {
		t.Fatalf("chatID = %d, want 9001", got)
	}
}

func TestBaldaHandlerOnForumTopicLifecycle_IgnoresEventWithoutTopicID(t *testing.T) {
	handler := &BaldaHandler{logger: zerolog.Nop(), channel: newBaldaTestTelegramAdapter()}

	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId: 66,
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
		},
	}

	if err := handler.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}
}

func TestBaldaHandlerOnMessage_IgnoresNilFrom(t *testing.T) {
	handler := &BaldaHandler{logger: zerolog.Nop(), channel: newBaldaTestTelegramAdapter()}
	handler.setOwner(101, 9001)

	text := "hello"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			Text: &text,
			From: nil,
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
}

func TestBaldaHandlerOnMessage_ChannelIgnoresNonMention(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "hello world"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestBaldaHandlerOnMessage_ChannelMentionBypassesGate(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	text := "@testbot hello world"
	entities := []client.MessageEntity{{Type: "mention", Offset: 0, Length: len("@testbot")}}
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text:     &text,
			Entities: &entities,
			From:     &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestBaldaHandlerOnMessage_DMNonMentionAllowed(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	text := "hello from dm"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestBaldaHandlerOnMessage_UnauthorizedUserRemainsSilent(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "try to enter the owner session"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			MessageId: 55,
			Text:      &text,
			From:      &client.User{Id: 202},
		},
	}

	if err := handler.onMessage(t.Context(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if got := len(turns.commandSnapshot()); got != 0 {
		t.Fatalf("published commands = %d, want 0", got)
	}
}

func TestBaldaHandlerOnMessage_CommandRemainsOnCommandPath(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "/reset"
	entities := []client.MessageEntity{{Type: "bot_command", Offset: 0, Length: len(text)}}
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			MessageId: 56,
			Text:      &text,
			Entities:  &entities,
			From:      &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(t.Context(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if got := len(turns.commandSnapshot()); got != 0 {
		t.Fatalf("published conversational commands = %d, want 0", got)
	}
}

func TestBaldaHandlerOnMessage_TopicUnknownThreadIgnoresNonMentionNonReply(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 77)

	text := "hello from the topic"
	topicID := 99
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			MessageThreadId: &topicID,
			Text:            &text,
			From:            &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestBaldaHandlerOnMessage_TopicKnownThreadStillRequiresMentionOrReply(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 77)

	text := "hello from the topic"
	topicID := 77
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			MessageThreadId: &topicID,
			Text:            &text,
			From:            &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestBaldaHandlerOnMessage_RejectsFalsePositiveBotMentionPrefix(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "@testbotx please ignore this"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestBaldaHandlerOnMessage_ChannelReplyToBotBypassesMentionGate(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	text := "following up in channel"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text:           &text,
			From:           &client.User{Id: 101},
			ReplyToMessage: replyToMessageFrom(4242, true),
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestBaldaHandlerOnMessage_ReplyAddsReplyContextToPublishedCommand(t *testing.T) {
	tests := []struct {
		name          string
		chatType      string
		replyToUserID int64
		replyToIsBot  bool
	}{
		{
			name:          "channel reply to bot",
			chatType:      "supergroup",
			replyToUserID: 4242,
			replyToIsBot:  true,
		},
		{
			name:          "dm reply to non-bot",
			chatType:      "private",
			replyToUserID: 777,
			replyToIsBot:  false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

			text := "сделай пр"
			replyText := "проверь этот коммит"
			event := &events.MessageEvent{
				Type: messagetype.Text,
				Message: &client.Message{
					Chat: client.Chat{
						Id:   9001,
						Type: tc.chatType,
					},
					Text:           &text,
					From:           &client.User{Id: 101},
					ReplyToMessage: replyToMessageWithTextFrom(tc.replyToUserID, tc.replyToIsBot, replyText),
				},
			}

			if err := handler.onMessage(context.Background(), event); err != nil {
				t.Fatalf("onMessage() error = %v", err)
			}
			assertPublishedTurnIncludesReplyContext(t, turns.commands, text, replyText)
		})
	}
}

func TestBaldaHandlerOnMessage_ForwardedBotMessageAddsForwardedContextToPublishedCommand(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "проверь этот коммит"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text: &text,
			From: &client.User{Id: 101},
			ForwardOrigin: &client.MessageOrigin{
				"type": "user",
				"user": map[string]interface{}{
					"id":     float64(4242),
					"is_bot": true,
				},
			},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if !strings.Contains(payload.Text, "Forwarded context:\n"+text) {
		t.Fatalf("payload text = %q, want forwarded context block", payload.Text)
	}
}

func TestBaldaHandlerOnMessage_TopicReplyToBotBypassesMentionGate(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 77)

	text := "topic follow up"
	topicID := 77
	isTopicMessage := true
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			Text:            &text,
			From:            &client.User{Id: 101},
			ReplyToMessage:  replyToMessageFrom(4242, true),
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
}

func TestBaldaHandlerOnMessage_PublishesDirectSessionTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	text := "run tests"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			Text: &text,
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if turns.commands[0].To.Target != baldaexecution.ActorTypeSession {
		t.Fatalf("command target = %q, want %q", turns.commands[0].To.Target, baldaexecution.ActorTypeSession)
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if payload.Source != "telegram" || !payload.Deliver {
		t.Fatalf("session turn payload = %+v, want telegram deliver=true", payload)
	}
}

func TestBaldaHandlerOnMessage_PublishesAttachmentOnlySessionTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	size := 81375
	event := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			Photo: &[]client.PhotoSize{
				{
					FileId:       "photo-file-id",
					FileUniqueId: "photo-unique-id",
					FileSize:     &size,
					Width:        900,
					Height:       896,
				},
			},
			From: &client.User{Id: 101},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(payload.Attachments))
	}
	if payload.Attachments[0].Kind != "photo" {
		t.Fatalf("attachment kind = %q, want photo", payload.Attachments[0].Kind)
	}
	if !strings.Contains(payload.Text, "Attachment manifest:") {
		t.Fatalf("payload text = %q, want attachment manifest", payload.Text)
	}
	if !strings.Contains(payload.Text, "kind: photo") {
		t.Fatalf("payload text = %q, want photo attachment metadata", payload.Text)
	}
}

func TestBaldaHandlerOnMessage_PublicAttachmentWithoutMentionOrReplyIsIgnored(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 77)

	topicID := 77
	isTopicMessage := true
	fileName := "context.json"
	event := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			From:            &client.User{Id: 101},
			Document: &client.Document{
				FileId:   "document-file-id",
				FileName: &fileName,
			},
		},
	}

	if err := handler.onMessage(t.Context(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func TestBaldaHandlerOnMessage_PublicAttachmentWithMentionPublishesTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 77)

	topicID := 77
	isTopicMessage := true
	fileName := "context.json"
	caption := "@testbot review this"
	captionEntities := []client.MessageEntity{{Type: "mention", Offset: 0, Length: len("@testbot")}}
	event := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			From:            &client.User{Id: 101},
			Caption:         &caption,
			CaptionEntities: &captionEntities,
			Document: &client.Document{
				FileId:   "document-file-id",
				FileName: &fileName,
			},
		},
	}

	if err := handler.onMessage(t.Context(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}

	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != "document" {
		t.Fatalf("attachments = %+v, want one document", payload.Attachments)
	}
	if !strings.Contains(payload.Text, "review this") ||
		!strings.Contains(payload.Text, "Attachment manifest:") ||
		!strings.Contains(payload.Text, "name: "+fileName) {
		t.Fatalf("payload text = %q, want mention text and document attachment manifest", payload.Text)
	}
}

func TestBaldaHandlerOnMessage_CoalescesTelegramMediaGroupIntoOneTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)
	turns.commandSignal = make(chan struct{}, 2)
	attachmentStore := &recordingTelegramAttachmentStore{}
	handler.attachmentStore = attachmentStore

	mediaGroupID := testTelegramMediaGroupID
	caption := "review both files"
	photoSize := 1024
	photoEvent := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    101,
			MediaGroupId: &mediaGroupID,
			Caption:      &caption,
			Photo: &[]client.PhotoSize{{
				FileId:       "photo-file-id",
				FileUniqueId: "photo-unique-id",
				FileSize:     &photoSize,
			}},
		},
	}
	documentName := "notes.txt"
	documentEvent := &events.MessageEvent{
		Type: messagetype.Document,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    102,
			MediaGroupId: &mediaGroupID,
			Document: &client.Document{
				FileId:       "document-file-id",
				FileUniqueId: "document-unique-id",
				FileName:     &documentName,
			},
		},
	}

	if err := handler.onMessage(t.Context(), photoEvent); err != nil {
		t.Fatalf("onMessage(photo) error = %v", err)
	}
	if err := handler.onMessage(t.Context(), documentEvent); err != nil {
		t.Fatalf("onMessage(document) error = %v", err)
	}
	if got := len(turns.commandSnapshot()); got != 0 {
		t.Fatalf("published commands before media-group flush = %d, want 0", got)
	}

	select {
	case <-turns.commandSignal:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for media-group turn")
	}
	select {
	case <-turns.commandSignal:
		t.Fatal("media group published more than one turn")
	case <-time.After(100 * time.Millisecond):
	}

	commands := turns.commandSnapshot()
	if len(commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(commands))
	}
	if got := baldaexecution.EnvelopeSessionID(commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	const wantDedupeKey = "telegram:9001:0:user:101:media-group:album-42"
	if commands[0].DedupeKey != wantDedupeKey {
		t.Fatalf("command dedupe key = %q, want %q", commands[0].DedupeKey, wantDedupeKey)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 2 {
		t.Fatalf("attachments = %d, want 2", len(payload.Attachments))
	}
	if len(attachmentStore.calls) != 1 || len(attachmentStore.calls[0]) != 2 {
		t.Fatalf("attachment persistence calls = %+v, want one call with complete media group", attachmentStore.calls)
	}
	if payload.Attachments[0].FileID != "photo-file-id" || payload.Attachments[1].FileID != "document-file-id" {
		t.Fatalf("attachments = %+v, want photo then document", payload.Attachments)
	}
	if !strings.Contains(payload.Text, "caption: "+caption) {
		t.Fatalf("payload text = %q, want media-group caption", payload.Text)
	}
	if payload.DedupeKey != wantDedupeKey || payload.RequesterUserID != "tg-101" {
		t.Fatalf("payload identity = %+v, want stable group identity and requester", payload)
	}
}

type recordingTelegramAttachmentStore struct {
	calls [][]attachment.Descriptor
}

func (s *recordingTelegramAttachmentStore) PersistTelegram(_ context.Context, in []attachment.Descriptor) ([]attachment.Descriptor, error) {
	s.calls = append(s.calls, append([]attachment.Descriptor(nil), in...))
	return attachment.NormalizeList(in), nil
}

func TestBaldaHandlerOnMessage_FlushesMediaGroupBeforeFollowingMessage(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	mediaGroupID := testTelegramMediaGroupID
	photoEvent := &events.MessageEvent{
		Type: messagetype.Photo,
		Message: &client.Message{
			Chat:         client.Chat{Id: 9001, Type: "private"},
			From:         &client.User{Id: 101},
			MessageId:    101,
			MediaGroupId: &mediaGroupID,
			Photo:        &[]client.PhotoSize{{FileId: "photo-file-id"}},
		},
	}
	text := "use the attached file"
	textEvent := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
			MessageId: 102,
			Text:      &text,
		},
	}

	if err := handler.onMessage(t.Context(), photoEvent); err != nil {
		t.Fatalf("onMessage(photo) error = %v", err)
	}
	if err := handler.onMessage(t.Context(), textEvent); err != nil {
		t.Fatalf("onMessage(text) error = %v", err)
	}

	commands := turns.commandSnapshot()
	if len(commands) != 2 {
		t.Fatalf("published commands = %d, want media group then text", len(commands))
	}
	var first actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[0].Payload, &first); err != nil {
		t.Fatalf("decode first session turn payload: %v", err)
	}
	var second actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[1].Payload, &second); err != nil {
		t.Fatalf("decode second session turn payload: %v", err)
	}
	if len(first.Attachments) != 1 || first.Attachments[0].FileID != "photo-file-id" {
		t.Fatalf("first turn attachments = %+v, want media group", first.Attachments)
	}
	if second.Text != text || len(second.Attachments) != 0 {
		t.Fatalf("second turn = %+v, want following text without attachments", second)
	}
}

func TestBaldaHandlerOnMessage_PublishesVoiceOnlySessionTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	size := int64(4096)
	event := &events.MessageEvent{
		Type: messagetype.Voice,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			From: &client.User{Id: 101},
			Voice: &client.Voice{
				FileId:       "voice-file-id",
				FileUniqueId: "voice-unique-id",
				FileSize:     &size,
			},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(payload.Attachments))
	}
	voice := payload.Attachments[0]
	if voice.Kind != "voice" || voice.FileID != "voice-file-id" || voice.SizeBytes != size {
		t.Fatalf("voice attachment = %+v", voice)
	}
	if !strings.Contains(payload.Text, "Attachment manifest:") || !strings.Contains(payload.Text, "kind: voice") {
		t.Fatalf("payload text = %q, want voice attachment manifest", payload.Text)
	}
}

func TestBaldaHandlerOnMessage_PublishesTextAndVoiceInOneSessionTurn(t *testing.T) {
	handler, turns, locator := newBaldaMessageHandlerHarness(t, 0)

	text := "please summarize this"
	caption := "voice context"
	event := &events.MessageEvent{
		Type: messagetype.Voice,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "private",
			},
			From:    &client.User{Id: 101},
			Text:    &text,
			Caption: &caption,
			Voice: &client.Voice{
				FileId: "voice-file-id",
			},
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}
	if len(turns.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(turns.commands))
	}
	if got := baldaexecution.EnvelopeSessionID(turns.commands[0]); got != locator.SessionID {
		t.Fatalf("command session = %q, want %q", got, locator.SessionID)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(turns.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode session turn payload: %v", err)
	}
	if len(payload.Attachments) != 1 || payload.Attachments[0].Kind != "voice" {
		t.Fatalf("attachments = %+v, want one voice attachment", payload.Attachments)
	}
	if !strings.Contains(payload.Text, text) || !strings.Contains(payload.Text, "kind: voice") || !strings.Contains(payload.Text, "caption: voice context") {
		t.Fatalf("payload text = %q, want text and voice manifest", payload.Text)
	}
}

func TestBaldaHandlerOnMessage_ChannelReplyToDifferentBotIgnored(t *testing.T) {
	handler, turns, _ := newBaldaMessageHandlerHarness(t, 0)

	text := "following up in channel"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   9001,
				Type: "supergroup",
			},
			Text:           &text,
			From:           &client.User{Id: 101},
			ReplyToMessage: replyToMessageFrom(9898, true),
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.commands) != 0 {
		t.Fatalf("published commands = %d, want 0", len(turns.commands))
	}
}

func newBaldaTestTelegramAdapter() *testTelegramChannel {
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	return &testTelegramChannel{Adapter: baldatelegram.NewAdapter(baldatelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})}
}

func newBaldaMessageHandlerHarness(t *testing.T, topicID int) (*BaldaHandler, *fakeTurnDispatcher, baldasession.SessionLocator) {
	t.Helper()

	stateStore := &fakeOwnerKVStore{}
	ownerStore, err := auth.NewOwnerStore(stateStore)
	if err != nil {
		t.Fatalf("NewOwnerStore(): %v", err)
	}
	if _, err := ownerStore.RegisterOwner(101, 9001); err != nil {
		t.Fatalf("RegisterOwner(): %v", err)
	}

	locator := baldatelegram.NewLocator(9001, topicID)
	sessionManager := newBaldaSessionManagerWithSession(t, locator, newBaldaTopicSession(t, locator.SessionID))
	turnDispatcher := &fakeTurnDispatcher{}
	handler := &BaldaHandler{
		ownerStore:      ownerStore,
		channel:         newBaldaTestTelegramAdapter(),
		sessionManager:  sessionManager,
		actorDispatcher: turnDispatcher,
		logger:          zerolog.Nop(),
	}
	handler.setOwner(101, 9001)
	setUnexportedField(t, handler, "baldaProviderName", "alpha")
	handler.botUsername = testBaldaBotUsername
	handler.botUserID = 4242

	return handler, turnDispatcher, locator
}

func replyToMessageFrom(userID int64, isBot bool) *client.Message {
	return &client.Message{
		MessageId: 7,
		From: &client.User{
			Id:    userID,
			IsBot: isBot,
		},
	}
}

func replyToMessageWithTextFrom(userID int64, isBot bool, text string) *client.Message {
	msg := replyToMessageFrom(userID, isBot)
	msg.Text = &text
	return msg
}

func assertPublishedTurnIncludesReplyContext(t *testing.T, commands []actorlayer.Envelope, text, replyText string) {
	t.Helper()
	if len(commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(commands))
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(commands[0].Payload, &payload); err != nil || strings.TrimSpace(payload.Text) == "" {
		var wrapped struct {
			Kind        string                     `json:"kind"`
			SessionTurn *actors.SessionTurnPayload `json:"session_turn,omitempty"`
		}
		if wrapErr := actorlayer.UnmarshalPayload(commands[0].Payload, &wrapped); wrapErr != nil {
			t.Fatalf("decode session turn payload: %v", err)
		}
		if wrapped.SessionTurn == nil {
			t.Fatalf("wrapped session turn payload missing: %+v", wrapped)
		}
		payload = *wrapped.SessionTurn
	}
	if !strings.Contains(payload.Text, "Reply context:\n"+replyText) {
		t.Fatalf("payload text = %q, want reply context block", payload.Text)
	}
	if !strings.Contains(payload.Text, "User message:\n"+text) {
		t.Fatalf("payload text = %q, want user message block", payload.Text)
	}
}

func newBaldaSessionManagerWithSession(t *testing.T, locator baldasession.SessionLocator, ts *baldasession.TopicSession) *baldasession.Manager {
	t.Helper()

	m := &baldasession.Manager{}
	setUnexportedField(t, m, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: ts})
	setUnexportedField(t, m, "sessionStore", &fakeBaldaRestoreSessionStore{})
	return m
}

func newBaldaTopicSession(t *testing.T, sessionID string) *baldasession.TopicSession {
	t.Helper()

	ts := &baldasession.TopicSession{}
	setUnexportedField(t, ts, "sessionID", sessionID)
	return ts
}

func setUnexportedField[T any](t *testing.T, target any, fieldName string, value T) {
	t.Helper()

	rv := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
