package handlersfx

import (
	"context"
	"testing"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/eventemitter"
	"github.com/tgbotkit/runtime/events"
	runtimehandlers "github.com/tgbotkit/runtime/handlers"
	"github.com/tgbotkit/runtime/messagetype"
)

type recordingCommandIngress struct {
	requests []commandcmd.Request
}

func (r *recordingCommandIngress) PublishCommand(_ context.Context, req commandcmd.Request) error {
	r.requests = append(r.requests, req)
	return nil
}

type fakeCommandRegistry struct {
	handler runtimehandlers.CommandHandler
}

func (r *fakeCommandRegistry) OnCommand(handler runtimehandlers.CommandHandler) eventemitter.UnsubscribeFunc {
	r.handler = handler
	return func() {}
}

func (r *fakeCommandRegistry) OnMessage(runtimehandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

func (r *fakeCommandRegistry) OnMessageType(messagetype.MessageType, runtimehandlers.MessageHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

func (r *fakeCommandRegistry) OnCallbackDataPrefix(string, runtimehandlers.CallbackQueryHandler) eventemitter.UnsubscribeFunc {
	return func() {}
}

type fakeTelegramChannel struct {
	ok bool
}

func (f *fakeTelegramChannel) CommandContextFromEvent(event *events.CommandEvent) (baldatelegram.CommandContext, bool) {
	if !f.ok {
		return baldatelegram.CommandContext{}, false
	}
	topicID := 0
	if event.Message != nil && event.Message.MessageThreadId != nil {
		topicID = *event.Message.MessageThreadId
	}
	chatID := int64(0)
	userID := int64(0)
	messageID := 0
	isDM := false
	if event.Message != nil {
		chatID = event.Message.Chat.Id
		messageID = event.Message.MessageId
		isDM = event.Message.Chat.Type == "private"
		if event.Message.From != nil {
			userID = event.Message.From.Id
		}
	}
	return baldatelegram.CommandContext{
		Locator:         telegramref.NewLocator(chatID, topicID),
		DeliveryOptions: deliveryfmt.Options{DeliveryFormat: deliveryfmt.DeliveryFormatMarkdown},
		ChatID:          chatID,
		TopicID:         topicID,
		UserID:          userID,
		MessageID:       messageID,
		Command:         event.Command,
		Args:            event.Args,
		IsDM:            isDM,
	}, true
}

func (f *fakeTelegramChannel) MessageContextFromEvent(*events.MessageEvent) (baldatelegram.MessageContext, bool) {
	return baldatelegram.MessageContext{}, false
}

func (f *fakeTelegramChannel) TopicLifecycleFromEvent(*events.MessageEvent) (baldatelegram.TopicLifecycleContext, bool) {
	return baldatelegram.TopicLifecycleContext{}, false
}

func (f *fakeTelegramChannel) CallbackContextFromEvent(*events.CallbackQueryEvent) (baldatelegram.CallbackContext, bool) {
	return baldatelegram.CallbackContext{}, false
}

func (f *fakeTelegramChannel) CollectMediaGroup(baldatelegram.MessageContext, func(context.Context, baldatelegram.MessageContext)) bool {
	return false
}

func (f *fakeTelegramChannel) CreateTopicLocator(context.Context, int64, string) (deliverycmd.Locator, error) {
	return deliverycmd.Locator{}, nil
}

func (f *fakeTelegramChannel) Close(context.Context, deliverycmd.Locator) error { return nil }

func (f *fakeTelegramChannel) AnswerQuestionCallback(context.Context, string, string, bool) error {
	return nil
}

func TestTelegramCommandHandler_PublishesActorOwnedCommands(t *testing.T) {
	supported := []string{"locator", "reset", "help", "usage", "auto", "cancel", "goalkeeper", "topic", "close", "user", "plugin"}

	for _, cmd := range supported {
		t.Run(cmd, func(t *testing.T) {
			ingress := &recordingCommandIngress{}
			channel := &fakeTelegramChannel{ok: true}
			handler := &telegramCommandHandler{
				channel:        channel,
				commandIngress: ingress,
				logger:         zerolog.Nop(),
			}

			event := &events.CommandEvent{
				Command: cmd,
				Args:    "sample-arg",
				Message: &client.Message{
					MessageId: 10,
					Chat:      client.Chat{Id: 9001, Type: "private"},
					From:      &client.User{Id: 101},
				},
			}

			if err := handler.onCommand(context.Background(), event); err != nil {
				t.Fatalf("onCommand() error: %v", err)
			}

			if len(ingress.requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(ingress.requests))
			}
			req := ingress.requests[0]
			if req.Payload.Name != cmd {
				t.Fatalf("req.Payload.Name = %q, want %q", req.Payload.Name, cmd)
			}
			if req.Payload.Args != "sample-arg" {
				t.Fatalf("req.Payload.Args = %q, want sample-arg", req.Payload.Args)
			}
		})
	}
}

func TestTelegramCommandHandler_AccessDerivation(t *testing.T) {
	t.Run("owner access", func(t *testing.T) {
		ownerStore := newTestOwnerStore(t)
		ingress := &recordingCommandIngress{}
		channel := &fakeTelegramChannel{ok: true}
		handler := &telegramCommandHandler{
			ownerStore:     ownerStore,
			channel:        channel,
			commandIngress: ingress,
			logger:         zerolog.Nop(),
		}

		event := &events.CommandEvent{
			Command: "help",
			Message: &client.Message{
				MessageId: 10,
				Chat:      client.Chat{Id: 9001, Type: "private"},
				From:      &client.User{Id: 101},
			},
		}

		if err := handler.onCommand(context.Background(), event); err != nil {
			t.Fatalf("onCommand() error: %v", err)
		}

		if len(ingress.requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(ingress.requests))
		}
		access := ingress.requests[0].Payload.Access
		if !access.Owner || !access.SessionCommands || access.Collaborator {
			t.Fatalf("unexpected access: %+v", access)
		}
	})

	t.Run("unauthorized access", func(t *testing.T) {
		ownerStore := newTestOwnerStore(t)
		ingress := &recordingCommandIngress{}
		channel := &fakeTelegramChannel{ok: true}
		handler := &telegramCommandHandler{
			ownerStore:     ownerStore,
			channel:        channel,
			commandIngress: ingress,
			logger:         zerolog.Nop(),
		}

		event := &events.CommandEvent{
			Command: "help",
			Message: &client.Message{
				MessageId: 10,
				Chat:      client.Chat{Id: 9001, Type: "private"},
				From:      &client.User{Id: 999},
			},
		}

		if err := handler.onCommand(context.Background(), event); err != nil {
			t.Fatalf("onCommand() error: %v", err)
		}

		if len(ingress.requests) != 1 {
			t.Fatalf("requests = %d, want 1", len(ingress.requests))
		}
		access := ingress.requests[0].Payload.Access
		if access.Owner || access.SessionCommands || access.Collaborator {
			t.Fatalf("unexpected access for unauthorized user: %+v", access)
		}
	})
}

func TestTelegramCommandHandler_IgnoresStartCommand(t *testing.T) {
	ingress := &recordingCommandIngress{}
	channel := &fakeTelegramChannel{ok: true}
	handler := &telegramCommandHandler{
		channel:        channel,
		commandIngress: ingress,
	}

	event := &events.CommandEvent{
		Command: "start",
		Message: &client.Message{
			MessageId: 10,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
		},
	}

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatalf("onCommand() error: %v", err)
	}

	if len(ingress.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(ingress.requests))
	}
}

func TestTelegramCommandHandler_IgnoresUnknownContext(t *testing.T) {
	ingress := &recordingCommandIngress{}
	channel := &fakeTelegramChannel{ok: false}
	handler := &telegramCommandHandler{
		channel:        channel,
		commandIngress: ingress,
	}

	event := &events.CommandEvent{
		Command: "help",
	}

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatalf("onCommand() error: %v", err)
	}

	if len(ingress.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(ingress.requests))
	}
}

func TestTelegramCommandHandler_Register(t *testing.T) {
	handler := &telegramCommandHandler{}
	registry := &fakeCommandRegistry{}

	var tgHandler tgbotkit.Handler = handler
	tgHandler.Register(registry)

	if registry.handler == nil {
		t.Fatal("registry handler was not registered")
	}
}

func TestTelegramStartHandler_PublishesActorOwnedCommand(t *testing.T) {
	ownerStore := newTestOwnerStore(t)
	ingress := &recordingCommandIngress{}
	handler := &telegramStartHandler{
		ownerStore:     ownerStore,
		commandIngress: ingress,
		logger:         zerolog.Nop(),
	}

	event := &events.CommandEvent{
		Command: "start",
		Args:    "token123",
		Message: &client.Message{
			MessageId: 15,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
		},
	}

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatalf("onCommand() error: %v", err)
	}

	if len(ingress.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(ingress.requests))
	}
	req := ingress.requests[0]
	if req.Payload.Name != "start" {
		t.Fatalf("req.Payload.Name = %q, want start", req.Payload.Name)
	}
	if req.Payload.Args != "token123" {
		t.Fatalf("req.Payload.Args = %q, want token123", req.Payload.Args)
	}
	if !req.Payload.Access.Owner {
		t.Fatal("expected Owner = true")
	}
}

func TestTelegramStartHandler_IgnoresNonPrivateChat(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &telegramStartHandler{
		commandIngress: ingress,
		logger:         zerolog.Nop(),
	}

	event := &events.CommandEvent{
		Command: "start",
		Message: &client.Message{
			MessageId: 15,
			Chat:      client.Chat{Id: 9001, Type: "supergroup"},
			From:      &client.User{Id: 101},
		},
	}

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatalf("onCommand() error: %v", err)
	}

	if len(ingress.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(ingress.requests))
	}
}

func TestTelegramStartHandler_IgnoresNonStartCommand(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &telegramStartHandler{
		commandIngress: ingress,
		logger:         zerolog.Nop(),
	}

	event := &events.CommandEvent{
		Command: "help",
		Message: &client.Message{
			MessageId: 15,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
		},
	}

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatalf("onCommand() error: %v", err)
	}

	if len(ingress.requests) != 0 {
		t.Fatalf("requests = %d, want 0", len(ingress.requests))
	}
}

func TestTelegramStartHandler_Register(t *testing.T) {
	handler := &telegramStartHandler{}
	registry := &fakeCommandRegistry{}

	var tgHandler tgbotkit.Handler = handler
	tgHandler.Register(registry)

	if registry.handler == nil {
		t.Fatal("registry handler was not registered")
	}
}
