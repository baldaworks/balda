package telegram

import (
	"context"
	"testing"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
)

type fakeRegistry struct {
	onMessageCalls   int
	callbackCalls    int
	messageTypeCalls []messagetype.MessageType
}

func (f *fakeRegistry) OnCommand(func(context.Context, *events.CommandEvent) error) {}
func (f *fakeRegistry) OnMessage(func(context.Context, *events.MessageEvent) error) {
	f.onMessageCalls++
}
func (f *fakeRegistry) OnMessageType(messageType messagetype.MessageType, _ func(context.Context, *events.MessageEvent) error) {
	f.messageTypeCalls = append(f.messageTypeCalls, messageType)
}
func (f *fakeRegistry) OnCallbackDataPrefix(string, func(context.Context, *events.CallbackQueryEvent) error) {
	f.callbackCalls++
}

func TestServerRegister_RegistersForumTopicMessageTypes(t *testing.T) {
	registry := &fakeRegistry{}
	server := &Server{logger: zerolog.Nop(), channel: &Adapter{}}

	server.Register(registry)

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

func TestServerOnForumTopicLifecycle_NonClosingEventsDoNotPublishControl(t *testing.T) {
	server := &Server{logger: zerolog.Nop(), channel: &Adapter{}, actorDispatcher: &fakeTurnDispatcher{}}
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
					Chat:            client.Chat{Id: 9001, Type: "supergroup"},
					From:            &client.User{Id: userID},
				},
			}

			if err := server.onForumTopicLifecycle(context.Background(), event); err != nil {
				t.Fatalf("onForumTopicLifecycle() error = %v", err)
			}
			if got := len(server.actorDispatcher.(*fakeTurnDispatcher).commands); got != 0 {
				t.Fatalf("published commands = %d, want 0", got)
			}
		})
	}
}

func TestServerOnForumTopicLifecycle_ClosedPublishesCancelControl(t *testing.T) {
	dispatcher := &fakeTurnDispatcher{}
	locator := NewLocator(9001, 77)
	sessionManager := newSessionManagerWithSession(t, locator, newTopicSession(t, locator.SessionID))
	server := &Server{
		logger:          zerolog.Nop(),
		channel:         &Adapter{},
		actorDispatcher: dispatcher,
		sessionManager:  sessionManager,
	}
	server.setChatID(9001)

	topicID := 77
	isTopicMessage := true
	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId:       42,
			MessageThreadId: &topicID,
			IsTopicMessage:  &isTopicMessage,
			Chat:            client.Chat{Id: 9001, Type: "supergroup"},
			From:            &client.User{Id: 101},
		},
	}

	if err := server.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("published commands = %d, want 1", len(dispatcher.commands))
	}
	if dispatcher.commands[0].Namespace != baldaexecution.NamespaceJobControl || dispatcher.commands[0].Kind != baldaexecution.KindCancel {
		t.Fatalf("published command = %+v, want job control cancel", dispatcher.commands[0])
	}
	if _, err := sessionManager.GetSession(locator); err == nil {
		t.Fatal("GetSession() error = nil, want stopped session")
	}
}

func TestServerOnForumTopicLifecycle_IgnoresOtherChatWhenBound(t *testing.T) {
	server := &Server{logger: zerolog.Nop(), channel: &Adapter{}}
	server.setChatID(9001)

	topicID := 13
	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId:       55,
			MessageThreadId: &topicID,
			Chat:            client.Chat{Id: 9999, Type: "supergroup"},
		},
	}

	if err := server.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}
	if got := server.getChatID(); got != 9001 {
		t.Fatalf("chatID = %d, want 9001", got)
	}
}

func TestServerOnForumTopicLifecycle_IgnoresEventWithoutTopicID(t *testing.T) {
	server := &Server{logger: zerolog.Nop(), channel: &Adapter{}}

	event := &events.MessageEvent{
		Type: messagetype.ForumTopicClosed,
		Message: &client.Message{
			MessageId: 66,
			Chat:      client.Chat{Id: 9001, Type: "supergroup"},
		},
	}

	if err := server.onForumTopicLifecycle(context.Background(), event); err != nil {
		t.Fatalf("onForumTopicLifecycle() error = %v", err)
	}
}
