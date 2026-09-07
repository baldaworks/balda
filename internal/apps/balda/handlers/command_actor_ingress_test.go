package handlers

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
)

type recordingCommandIngress struct {
	requests []commandcmd.Request
}

func (r *recordingCommandIngress) PublishCommand(_ context.Context, req commandcmd.Request) error {
	r.requests = append(r.requests, req)
	return nil
}

type fakeCommandRegistry struct {
	handler func(context.Context, *events.CommandEvent) error
}

func (r *fakeCommandRegistry) OnCommand(handler func(context.Context, *events.CommandEvent) error) {
	r.handler = handler
}

type fakeCommandChannel struct {
	ok bool
}

func (f *fakeCommandChannel) CommandContextFromEvent(event *events.CommandEvent) (CommandContext, bool) {
	if !f.ok {
		return CommandContext{}, false
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
	return CommandContext{
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

type fakeKVStore struct {
	data map[string]any
}

func (s *fakeKVStore) GetJSON(_ context.Context, key string) (any, bool, error) {
	if s.data == nil {
		return nil, false, nil
	}
	val, ok := s.data[key]
	return val, ok, nil
}

func (s *fakeKVStore) SetJSON(_ context.Context, key string, value any) error {
	if s.data == nil {
		s.data = make(map[string]any)
	}
	s.data[key] = value
	return nil
}

type fakeCollaboratorBackend struct {
	collaborators map[string]auth.Collaborator
}

func (b *fakeCollaboratorBackend) AddCollaborator(_ context.Context, c auth.Collaborator) error {
	if b.collaborators == nil {
		b.collaborators = make(map[string]auth.Collaborator)
	}
	b.collaborators[c.UserID] = c
	return nil
}

func (b *fakeCollaboratorBackend) RemoveCollaborator(_ context.Context, userID string) error {
	delete(b.collaborators, userID)
	return nil
}

func (b *fakeCollaboratorBackend) GetCollaborator(_ context.Context, userID string) (*auth.Collaborator, bool, error) {
	c, ok := b.collaborators[userID]
	if !ok {
		return nil, false, nil
	}
	return &c, true, nil
}

func (b *fakeCollaboratorBackend) ListCollaborators(_ context.Context) ([]auth.Collaborator, error) {
	var list []auth.Collaborator
	for _, c := range b.collaborators {
		list = append(list, c)
	}
	return list, nil
}

func newCommandEvent(command, args string, userID, chatID int64, topicID *int) *events.CommandEvent {
	return &events.CommandEvent{
		Command: command,
		Args:    args,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   chatID,
				Type: "group",
			},
			From: &client.User{
				Id: userID,
			},
			MessageThreadId: topicID,
		},
	}
}

func newStartEvent(args string, userID, chatID int64) *events.CommandEvent {
	return &events.CommandEvent{
		Command: commandStart,
		Args:    args,
		Message: &client.Message{
			Chat: client.Chat{
				Id:   chatID,
				Type: chatTypePrivate,
			},
			From: &client.User{
				Id: userID,
			},
		},
	}
}

func TestCommandHandlerPublishesActorOwnedCommands(t *testing.T) {
	ownerStore, err := auth.NewOwnerStore(&fakeKVStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerStore.RegisterOwner(101, 9001); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"locator", "reset", "help", "usage", "auto", "cancel", "goalkeeper", "topic", "close", "user", "plugin"} {
		t.Run(name, func(t *testing.T) {
			ingress := &recordingCommandIngress{}
			handler := &CommandHandler{
				ownerStore:     ownerStore,
				channel:        &fakeCommandChannel{ok: true},
				commandIngress: ingress,
			}
			event := newCommandEvent(name, "", 101, 9001, nil)
			event.Message.MessageId = 77

			if err := handler.onCommand(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if len(ingress.requests) != 1 {
				t.Fatalf("published requests = %d, want 1", len(ingress.requests))
			}
			got := ingress.requests[0]
			if got.InvocationID != "telegram:command:9001:77" || got.Payload.Name != name || !got.Payload.Access.Owner {
				t.Fatalf("published request = %+v", got)
			}
		})
	}
}

func TestCommandHandlerAccessDerivation(t *testing.T) {
	ownerStore, err := auth.NewOwnerStore(&fakeKVStore{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ownerStore.RegisterOwner(101, 9001); err != nil {
		t.Fatal(err)
	}

	collabBackend := &fakeCollaboratorBackend{
		collaborators: map[string]auth.Collaborator{
			"102": {UserID: "102"},
		},
	}
	collabStore := auth.NewCollaboratorStore(collabBackend)

	tests := []struct {
		name       string
		userID     int64
		wantAccess commandcmd.Access
	}{
		{
			name:   "owner access",
			userID: 101,
			wantAccess: commandcmd.Access{
				SessionCommands: true,
				Owner:           true,
				Collaborator:    false,
			},
		},
		{
			name:   "collaborator access",
			userID: 102,
			wantAccess: commandcmd.Access{
				SessionCommands: true,
				Owner:           false,
				Collaborator:    true,
			},
		},
		{
			name:   "unauthorized access",
			userID: 103,
			wantAccess: commandcmd.Access{
				SessionCommands: false,
				Owner:           false,
				Collaborator:    false,
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ingress := &recordingCommandIngress{}
			handler := &CommandHandler{
				ownerStore:        ownerStore,
				collaboratorStore: collabStore,
				channel:           &fakeCommandChannel{ok: true},
				commandIngress:    ingress,
			}
			event := newCommandEvent("help", "", tc.userID, 9001, nil)
			event.Message.MessageId = 42

			if err := handler.onCommand(context.Background(), event); err != nil {
				t.Fatal(err)
			}
			if len(ingress.requests) != 1 {
				t.Fatalf("requests count = %d, want 1", len(ingress.requests))
			}
			got := ingress.requests[0].Payload.Access
			if got != tc.wantAccess {
				t.Fatalf("access = %+v, want %+v", got, tc.wantAccess)
			}
		})
	}
}

func TestCommandHandlerIgnoresStartCommand(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &CommandHandler{
		channel:        &fakeCommandChannel{ok: true},
		commandIngress: ingress,
	}
	event := newCommandEvent(commandStart, "", 101, 9001, nil)
	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(ingress.requests) != 0 {
		t.Fatalf("expected 0 published requests for start, got %d", len(ingress.requests))
	}
}

func TestCommandHandlerIgnoresUnknownContext(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &CommandHandler{
		channel:        &fakeCommandChannel{ok: false},
		commandIngress: ingress,
	}
	event := newCommandEvent("help", "", 101, 9001, nil)
	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(ingress.requests) != 0 {
		t.Fatalf("expected 0 published requests when channel context fails, got %d", len(ingress.requests))
	}
}

func TestCommandHandlerRegister(t *testing.T) {
	reg := &fakeCommandRegistry{}
	handler := &CommandHandler{}
	handler.Register(reg)
	if reg.handler == nil {
		t.Fatal("expected handler to be registered")
	}
}

func TestStartHandlerPublishesActorOwnedCommand(t *testing.T) {
	ownerStore, err := auth.NewOwnerStore(&fakeKVStore{})
	if err != nil {
		t.Fatal(err)
	}

	ingress := &recordingCommandIngress{}
	handler := &StartHandler{
		ownerStore:     ownerStore,
		commandIngress: ingress,
	}
	event := newStartEvent("owner=secret-token", 101, 9001)
	event.Message.MessageId = 77

	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(ingress.requests) != 1 {
		t.Fatalf("published requests = %d, want 1", len(ingress.requests))
	}
	got := ingress.requests[0]
	if got.InvocationID != "telegram:command:9001:77" || got.Payload.Name != "start" || got.Payload.Args != "owner=secret-token" {
		t.Fatalf("published request = %+v", got)
	}
	if ownerStore.HasOwner() {
		t.Fatal("ingress executed owner policy before actor")
	}
}

func TestStartHandlerIgnoresNonPrivateChat(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &StartHandler{commandIngress: ingress}
	event := &events.CommandEvent{
		Command: commandStart,
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: "group"},
			From: &client.User{Id: 101},
		},
	}
	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(ingress.requests) != 0 {
		t.Fatalf("expected 0 requests for non-private chat, got %d", len(ingress.requests))
	}
}

func TestStartHandlerIgnoresNonStartCommand(t *testing.T) {
	ingress := &recordingCommandIngress{}
	handler := &StartHandler{commandIngress: ingress}
	event := &events.CommandEvent{
		Command: "other",
		Message: &client.Message{
			Chat: client.Chat{Id: 9001, Type: chatTypePrivate},
			From: &client.User{Id: 101},
		},
	}
	if err := handler.onCommand(context.Background(), event); err != nil {
		t.Fatal(err)
	}
	if len(ingress.requests) != 0 {
		t.Fatalf("expected 0 requests for non-start command, got %d", len(ingress.requests))
	}
}

func TestStartHandlerRegister(t *testing.T) {
	reg := &fakeCommandRegistry{}
	handler := &StartHandler{}
	handler.Register(reg)
	if reg.handler == nil {
		t.Fatal("expected handler to be registered")
	}
}
