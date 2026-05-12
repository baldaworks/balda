package handlers

import (
	"context"
	"reflect"
	"testing"
	"unsafe"

	"github.com/normahq/relay/internal/apps/relay/auth"
	relaytelegram "github.com/normahq/relay/internal/apps/relay/channel/telegram"
	"github.com/normahq/relay/internal/apps/relay/messenger"
	relaysession "github.com/normahq/relay/internal/apps/relay/session"
	"github.com/normahq/relay/internal/apps/relay/tgbotkit"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
	rtHandlers "github.com/tgbotkit/runtime/handlers"
	"github.com/tgbotkit/runtime/messagetype"
)

var _ tgbotkit.Registry = (*fakeRelayRegistry)(nil)

type fakeRelayRegistry struct {
	onMessageCalls   int
	messageTypeCalls []messagetype.MessageType
}

func (f *fakeRelayRegistry) OnUpdate(rtHandlers.UpdateHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

func (f *fakeRelayRegistry) OnMessage(rtHandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	f.onMessageCalls++
	return func() {}
}

func (f *fakeRelayRegistry) OnMessageType(t messagetype.MessageType, _ rtHandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	f.messageTypeCalls = append(f.messageTypeCalls, t)
	return func() {}
}

func (f *fakeRelayRegistry) OnCommand(rtHandlers.CommandHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

type fakeRelayAuthorizer struct {
	ownerID        int64
	isCollaborator bool
}

func (f *fakeRelayAuthorizer) IsOwner(userID int64) bool {
	return userID == f.ownerID
}

func (f *fakeRelayAuthorizer) IsCollaborator(userID int64) bool {
	return f.isCollaborator
}

func TestRelayHandlerRegister_RegistersForumTopicMessageTypes(t *testing.T) {
	registry := &fakeRelayRegistry{}
	handler := &RelayHandler{logger: zerolog.Nop(), channel: newRelayTestTelegramAdapter()}

	handler.Register(registry)

	if registry.onMessageCalls != 1 {
		t.Fatalf("OnMessage calls = %d, want 1", registry.onMessageCalls)
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

func TestRelayHandlerOnForumTopicLifecycle_LogOnly(t *testing.T) {
	handler := &RelayHandler{logger: zerolog.Nop(), channel: newRelayTestTelegramAdapter()}

	tests := []messagetype.MessageType{
		messagetype.ForumTopicCreated,
		messagetype.ForumTopicEdited,
		messagetype.ForumTopicClosed,
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

func TestRelayHandlerOnForumTopicLifecycle_IgnoresOtherChatWhenBound(t *testing.T) {
	handler := &RelayHandler{logger: zerolog.Nop(), channel: newRelayTestTelegramAdapter()}
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

func TestRelayHandlerOnForumTopicLifecycle_IgnoresEventWithoutTopicID(t *testing.T) {
	handler := &RelayHandler{logger: zerolog.Nop(), channel: newRelayTestTelegramAdapter()}

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

func TestRelayHandlerOnMessage_IgnoresNilFrom(t *testing.T) {
	handler := &RelayHandler{logger: zerolog.Nop(), channel: newRelayTestTelegramAdapter()}
	handler.SetOwner(101, 9001)

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

func TestRelayHandlerOnMessage_ChannelIgnoresNonMention(t *testing.T) {
	handler, turns, _ := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(turns.enqueueCalls))
	}
}

func TestRelayHandlerOnMessage_ChannelMentionBypassesGate(t *testing.T) {
	handler, turns, locator := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
	if turns.enqueueCalls[0].SessionID != locator.SessionID {
		t.Fatalf("Enqueue session = %q, want %q", turns.enqueueCalls[0].SessionID, locator.SessionID)
	}
}

func TestRelayHandlerOnMessage_DMNonMentionAllowed(t *testing.T) {
	handler, turns, locator := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
	if turns.enqueueCalls[0].SessionID != locator.SessionID {
		t.Fatalf("Enqueue session = %q, want %q", turns.enqueueCalls[0].SessionID, locator.SessionID)
	}
}

func TestRelayHandlerOnMessage_TopicUnknownThreadIgnoresNonMentionNonReply(t *testing.T) {
	handler, turns, _ := newRelayMessageHandlerHarness(t, 77)

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

	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(turns.enqueueCalls))
	}
}

func TestRelayHandlerOnMessage_TopicKnownThreadStillRequiresMentionOrReply(t *testing.T) {
	handler, turns, _ := newRelayMessageHandlerHarness(t, 77)

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

	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(turns.enqueueCalls))
	}
}

func TestRelayHandlerOnMessage_RejectsFalsePositiveBotMentionPrefix(t *testing.T) {
	handler, turns, _ := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(turns.enqueueCalls))
	}
}

func TestRelayHandlerOnMessage_ChannelReplyToBotBypassesMentionGate(t *testing.T) {
	handler, turns, locator := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
	if turns.enqueueCalls[0].SessionID != locator.SessionID {
		t.Fatalf("Enqueue session = %q, want %q", turns.enqueueCalls[0].SessionID, locator.SessionID)
	}
}

func TestRelayHandlerOnMessage_TopicReplyToBotBypassesMentionGate(t *testing.T) {
	handler, turns, locator := newRelayMessageHandlerHarness(t, 77)

	text := "topic follow up"
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
			ReplyToMessage:  replyToMessageFrom(4242, true),
		},
	}

	if err := handler.onMessage(context.Background(), event); err != nil {
		t.Fatalf("onMessage() error = %v", err)
	}

	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
	if turns.enqueueCalls[0].SessionID != locator.SessionID {
		t.Fatalf("Enqueue session = %q, want %q", turns.enqueueCalls[0].SessionID, locator.SessionID)
	}
}

func TestRelayHandlerOnMessage_ChannelReplyToDifferentBotIgnored(t *testing.T) {
	handler, turns, _ := newRelayMessageHandlerHarness(t, 0)

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

	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0", len(turns.enqueueCalls))
	}
}

func newRelayTestTelegramAdapter() *relaytelegram.Adapter {
	tgClient := &fakeTelegramClient{}
	msg := messenger.NewMessenger(tgClient, zerolog.Nop())
	return relaytelegram.NewAdapter(relaytelegram.AdapterParams{
		Messenger: msg,
		TGClient:  tgClient,
		Logger:    zerolog.Nop(),
	})
}

func newRelayMessageHandlerHarness(t *testing.T, topicID int) (*RelayHandler, *fakeTurnDispatcher, relaysession.SessionLocator) {
	t.Helper()

	stateStore := &fakeOwnerKVStore{}
	ownerStore, err := auth.NewOwnerStore(stateStore)
	if err != nil {
		t.Fatalf("NewOwnerStore(): %v", err)
	}
	if _, err := ownerStore.RegisterOwner(101, 9001, "owner", "Owner", "", true); err != nil {
		t.Fatalf("RegisterOwner(): %v", err)
	}

	locator := relaysession.NewTelegramSessionLocator(9001, topicID)
	sessionManager := newRelaySessionManagerWithSession(t, locator, newRelayTopicSession(t, locator.SessionID))
	turnDispatcher := &fakeTurnDispatcher{}
	handler := &RelayHandler{
		ownerStore:     ownerStore,
		channel:        newRelayTestTelegramAdapter(),
		sessionManager: sessionManager,
		turnDispatcher: turnDispatcher,
		logger:         zerolog.Nop(),
		authorizer:     &fakeRelayAuthorizer{ownerID: 101},
	}
	handler.SetOwner(101, 9001)
	setUnexportedField(t, handler, "relayProviderName", "alpha")
	handler.botUsername = testRelayBotUsername
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

func newRelaySessionManagerWithSession(t *testing.T, locator relaysession.SessionLocator, ts *relaysession.TopicSession) *relaysession.Manager {
	t.Helper()

	m := &relaysession.Manager{}
	setUnexportedField(t, m, "sessions", map[string]*relaysession.TopicSession{locator.SessionID: ts})
	return m
}

func newRelayTopicSession(t *testing.T, sessionID string) *relaysession.TopicSession {
	t.Helper()

	ts := &relaysession.TopicSession{}
	setUnexportedField(t, ts, "sessionID", sessionID)
	return ts
}

func setUnexportedField[T any](t *testing.T, target any, fieldName string, value T) {
	t.Helper()

	rv := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}
