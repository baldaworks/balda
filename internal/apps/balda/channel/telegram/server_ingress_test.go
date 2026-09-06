package telegram

import (
	"context"
	"testing"

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

type fakeInboundHandler struct {
	messageCalls  []MessageContext
	callbackCalls []CallbackContext
	topicCalls    []TopicLifecycleContext
}

func (f *fakeInboundHandler) HandleMessage(_ context.Context, message MessageContext) error {
	f.messageCalls = append(f.messageCalls, message)
	return nil
}

func (f *fakeInboundHandler) HandleCallback(_ context.Context, callback CallbackContext) error {
	f.callbackCalls = append(f.callbackCalls, callback)
	return nil
}

func (f *fakeInboundHandler) HandleForumTopic(_ context.Context, lifecycle TopicLifecycleContext) error {
	f.topicCalls = append(f.topicCalls, lifecycle)
	return nil
}

type fakeLifecycleHandler struct {
	startedCalls []struct {
		botUserID   int64
		botUsername string
	}
}

func (f *fakeLifecycleHandler) OnBotStarted(_ context.Context, botUserID int64, botUsername string) error {
	f.startedCalls = append(f.startedCalls, struct {
		botUserID   int64
		botUsername string
	}{botUserID: botUserID, botUsername: botUsername})
	return nil
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

func TestServerStart_InitializesBotIdentityAndNotifiesLifecycle(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	lifecycle := &fakeLifecycleHandler{}
	server := NewServer(ServerParams{
		Channel:          &Adapter{},
		LifecycleHandler: lifecycle,
		TGClient:         tgClient,
		TelegramEnabled:  true,
		Logger:           zerolog.Nop(),
	})

	if err := server.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	botID, botUser := server.GetBotIdentity()
	if botID != 4242 || botUser != testServerBotUsername {
		t.Fatalf("bot identity = (%d, %s), want (4242, %s)", botID, botUser, testServerBotUsername)
	}

	if len(lifecycle.startedCalls) != 1 {
		t.Fatalf("OnBotStarted calls = %d, want 1", len(lifecycle.startedCalls))
	}
	if lifecycle.startedCalls[0].botUserID != 4242 || lifecycle.startedCalls[0].botUsername != testServerBotUsername {
		t.Fatalf("OnBotStarted call = %+v, want botID=4242, botUser=%s", lifecycle.startedCalls[0], testServerBotUsername)
	}
}

func TestServerHandleMessage_ForwardsToInboundHandler(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "none")
	inbound := &fakeInboundHandler{}
	server := NewServer(ServerParams{
		Channel:         adapter,
		InboundHandler:  inbound,
		TGClient:        tgClient,
		TelegramEnabled: true,
		Logger:          zerolog.Nop(),
	})

	text := "hello bot"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			MessageId: 42,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
			Text:      &text,
		},
	}

	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	if len(inbound.messageCalls) != 1 {
		t.Fatalf("HandleMessage calls = %d, want 1", len(inbound.messageCalls))
	}
	if inbound.messageCalls[0].MessageID != 42 {
		t.Fatalf("message ID = %d, want 42", inbound.messageCalls[0].MessageID)
	}
}

func TestServerHandleMessage_CommandIgnored(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "none")
	inbound := &fakeInboundHandler{}
	server := NewServer(ServerParams{
		Channel:         adapter,
		InboundHandler:  inbound,
		TGClient:        tgClient,
		TelegramEnabled: true,
		Logger:          zerolog.Nop(),
	})

	cmdText := "/reset"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			MessageId: 43,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
			Text:      &cmdText,
			Entities: &[]client.MessageEntity{
				{Type: "bot_command", Offset: 0, Length: 6},
			},
		},
	}

	if err := server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	if len(inbound.messageCalls) != 0 {
		t.Fatalf("HandleMessage calls = %d, want 0 for command", len(inbound.messageCalls))
	}
}

func TestServerHandleQuestionCallback_ForwardsToInboundHandler(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "none")
	inbound := &fakeInboundHandler{}
	server := NewServer(ServerParams{
		Channel:         adapter,
		InboundHandler:  inbound,
		TGClient:        tgClient,
		TelegramEnabled: true,
		Logger:          zerolog.Nop(),
	})

	data := QuestionCallbackPrefix + "q123:1"
	msg := client.MaybeInaccessibleMessage{
		"message_id": 99,
		"chat": map[string]any{
			"id":   int64(9001),
			"type": "private",
		},
	}
	event := &events.CallbackQueryEvent{
		CallbackQuery: &client.CallbackQuery{
			Id:      "cb123",
			From:    client.User{Id: 101},
			Data:    &data,
			Message: &msg,
		},
	}

	if err := server.HandleQuestionCallback(context.Background(), event); err != nil {
		t.Fatalf("HandleQuestionCallback() error = %v", err)
	}

	if len(inbound.callbackCalls) != 1 {
		t.Fatalf("HandleCallback calls = %d, want 1", len(inbound.callbackCalls))
	}
	if inbound.callbackCalls[0].QuestionID != "q123" {
		t.Fatalf("QuestionID = %q, want q123", inbound.callbackCalls[0].QuestionID)
	}
}

func TestServerOnForumTopicLifecycle_ForwardsToInboundHandler(t *testing.T) {
	tgClient := &fakeTelegramClient{}
	adapter := newTestAdapter(tgClient, "none")
	inbound := &fakeInboundHandler{}
	server := NewServer(ServerParams{
		Channel:         adapter,
		InboundHandler:  inbound,
		TGClient:        tgClient,
		TelegramEnabled: true,
		Logger:          zerolog.Nop(),
	})

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

	if len(inbound.topicCalls) != 1 {
		t.Fatalf("HandleForumTopic calls = %d, want 1", len(inbound.topicCalls))
	}
	if inbound.topicCalls[0].TopicID != 77 {
		t.Fatalf("TopicID = %d, want 77", inbound.topicCalls[0].TopicID)
	}
}
