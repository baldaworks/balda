package start

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeOwnerStore struct {
	hasOwner     bool
	ownerID      int64
	ownerChatID  int64
	subjects     map[string]bool
	registerErr  error
	bindTelegram error
	bindSubject  error
}

func newFakeOwnerStore() *fakeOwnerStore {
	return &fakeOwnerStore{subjects: make(map[string]bool)}
}

func (s *fakeOwnerStore) HasOwner() bool { return s.hasOwner }

func (s *fakeOwnerStore) IsOwner(userID int64) bool {
	return s.hasOwner && s.ownerID == userID
}

func (s *fakeOwnerStore) IsOwnerSubject(subject string) bool {
	return s.hasOwner && s.subjects[subject]
}

func (s *fakeOwnerStore) RegisterOwner(userID, chatID int64) (bool, error) {
	if s.registerErr != nil {
		return false, s.registerErr
	}
	if s.hasOwner {
		return false, nil
	}
	s.hasOwner = true
	s.ownerID = userID
	s.ownerChatID = chatID
	s.subjects[auth.TelegramSubject(userID)] = true
	return true, nil
}

func (s *fakeOwnerStore) RegisterOwnerSubject(subject string) (bool, error) {
	if s.registerErr != nil {
		return false, s.registerErr
	}
	if s.hasOwner {
		return false, nil
	}
	s.hasOwner = true
	s.subjects[subject] = true
	return true, nil
}

func (s *fakeOwnerStore) BindOwnerTelegram(userID, chatID int64) error {
	if s.bindTelegram != nil {
		return s.bindTelegram
	}
	s.ownerID = userID
	s.ownerChatID = chatID
	s.subjects[auth.TelegramSubject(userID)] = true
	return nil
}

func (s *fakeOwnerStore) BindOwnerSubject(subject string) error {
	if s.bindSubject != nil {
		return s.bindSubject
	}
	s.subjects[subject] = true
	return nil
}

type fakeInviteStore struct {
	invites map[string]*auth.Invite
	err     error
}

func (s *fakeInviteStore) GetInvite(_ context.Context, token string) (*auth.Invite, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.invites[token], nil
}

type fakeCollaboratorStore struct {
	collaborators map[string]*auth.Collaborator
	getErr        error
	addErr        error
}

func newFakeCollaboratorStore() *fakeCollaboratorStore {
	return &fakeCollaboratorStore{collaborators: make(map[string]*auth.Collaborator)}
}

func (s *fakeCollaboratorStore) GetCollaborator(_ context.Context, userID string) (*auth.Collaborator, bool, error) {
	if s.getErr != nil {
		return nil, false, s.getErr
	}
	c, ok := s.collaborators[userID]
	return c, ok, nil
}

func (s *fakeCollaboratorStore) AddCollaborator(_ context.Context, c auth.Collaborator) error {
	if s.addErr != nil {
		return s.addErr
	}
	s.collaborators[c.UserID] = &c
	return nil
}

type fakeChannelAuthService struct {
	consumedTokens map[string]bool
	tokens         []auth.OwnerBindToken
	consumeErr     error
	missingErr     error
}

func (s *fakeChannelAuthService) ConsumeOwnerBind(_ context.Context, _, _, token string) (bool, error) {
	if s.consumeErr != nil {
		return false, s.consumeErr
	}
	return s.consumedTokens[token], nil
}

func (s *fakeChannelAuthService) CreateMissingOwnerBindTokens(_ context.Context, _ string) ([]auth.OwnerBindToken, error) {
	if s.missingErr != nil {
		return nil, s.missingErr
	}
	return s.tokens, nil
}

type fakeOwnerActivator struct {
	activated []deliverycmd.Locator
	err       error
}

func (a *fakeOwnerActivator) ActivateOwner(_ context.Context, locator deliverycmd.Locator, _ string) error {
	a.activated = append(a.activated, locator)
	return a.err
}

type recordingDispatcher struct {
	sentEnvelopes []actorlayer.Envelope
}

func (d *recordingDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.sentEnvelopes = append(d.sentEnvelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func (d *recordingDispatcher) lastText(t *testing.T) string {
	t.Helper()
	if len(d.sentEnvelopes) == 0 {
		t.Fatal("no envelopes dispatched")
	}
	last := d.sentEnvelopes[len(d.sentEnvelopes)-1]
	var payload actors.DeliveryPayload
	if err := actorlayer.UnmarshalPayload(last.Payload, &payload); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	return payload.Text
}

func telegramLocator(chatID int64) deliverycmd.Locator {
	return telegramref.NewLocator(chatID, 0)
}

func zulipLocator(_ int) deliverycmd.Locator {
	return deliverycmd.Locator{
		ChannelType: "zulip",
		AddressKey:  "dm:101",
		SessionID:   "zulip-dm-101",
	}
}

func TestStartHandler_Name(t *testing.T) {
	h := New(nil, nil, nil, nil, nil, nil, "", zerolog.Nop())
	if got := h.Name(); got != "start" {
		t.Fatalf("h.Name() = %q, want start", got)
	}
}

func TestStartHandler_DirectMessagePolicy(t *testing.T) {
	t.Run("telegram ignores non-DM", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		h := New(nil, nil, nil, nil, nil, dispatcher, "token", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "telegram",
			Conversation: commandcmd.Conversation{Direct: false},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if len(dispatcher.sentEnvelopes) != 0 {
			t.Fatalf("sent envelopes = %d, want 0", len(dispatcher.sentEnvelopes))
		}
	})

	t.Run("zulip rejects non-DM", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		h := New(nil, nil, nil, nil, nil, dispatcher, "token", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "zulip",
			Conversation: commandcmd.Conversation{Direct: false},
			Locator:      zulipLocator(101),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "direct messages") {
			t.Fatalf("last text = %q, want direct messages error", text)
		}
	})
}

func TestStartHandler_MalformedArgs(t *testing.T) {
	dispatcher := &recordingDispatcher{}
	h := New(nil, nil, nil, nil, nil, dispatcher, "token", zerolog.Nop())
	env := actorlayer.Envelope{ID: "env-1"}

	tests := []struct {
		name      string
		args      string
		transport string
		wantSub   string
	}{
		{
			name:      "telegram extra args",
			args:      "foo bar",
			transport: "telegram",
			wantSub:   "Invalid /start format",
		},
		{
			name:      "telegram query prefix",
			args:      "?start=owner_tok",
			transport: "telegram",
			wantSub:   "Invalid /start format",
		},
		{
			name:      "telegram missing value",
			args:      "owner=",
			transport: "telegram",
			wantSub:   "Invalid /start format",
		},
		{
			name:      "zulip extra args",
			args:      "owner=tok extra",
			transport: "zulip",
			wantSub:   "Invalid /start format",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			payload := commandcmd.Payload{
				Name:         "start",
				Args:         tt.args,
				Transport:    tt.transport,
				Conversation: commandcmd.Conversation{Direct: true},
				Locator:      telegramLocator(9001),
			}
			if err := h.Handle(context.Background(), env, payload); err != nil {
				t.Fatal(err)
			}
			if text := dispatcher.lastText(t); !strings.Contains(text, tt.wantSub) {
				t.Fatalf("last text = %q, want substring %q", text, tt.wantSub)
			}
		})
	}
}

func TestStartHandler_WelcomeMessageWhenUnregistered(t *testing.T) {
	dispatcher := &recordingDispatcher{}
	owners := newFakeOwnerStore()
	h := New(owners, nil, nil, nil, nil, dispatcher, "token", zerolog.Nop())
	env := actorlayer.Envelope{ID: "env-1"}

	t.Run("telegram welcome", func(t *testing.T) {
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "telegram",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Welcome to Balda Bot!") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("zulip welcome", func(t *testing.T) {
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "zulip",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      zulipLocator(101),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Welcome to Balda Bot!") {
			t.Fatalf("last text = %q", text)
		}
	})
}

func TestStartHandler_OwnerBootstrapFlow(t *testing.T) {
	t.Run("telegram owner bootstrap success", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		activator := &fakeOwnerActivator{}
		h := New(owners, nil, nil, nil, activator, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "owner=secret-123",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if !owners.HasOwner() {
			t.Fatal("owner not registered")
		}
		if !owners.IsOwner(101) {
			t.Fatal("owner ID 101 not registered")
		}
		if len(activator.activated) != 1 {
			t.Fatalf("activator calls = %d, want 1", len(activator.activated))
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Congratulations") {
			t.Fatalf("last text = %q, want Congratulations", text)
		}
	})

	t.Run("telegram owner auth link prefix success", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		h := New(owners, nil, nil, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "owner_secret-123",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if !owners.HasOwner() {
			t.Fatal("owner not registered")
		}
	})

	t.Run("zulip owner bootstrap success", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		activator := &fakeOwnerActivator{}
		h := New(owners, nil, nil, nil, activator, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "owner=secret-123",
			Transport:    "zulip",
			Principal:    "202",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      zulipLocator(202),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if !owners.HasOwner() {
			t.Fatal("owner not registered")
		}
		if !owners.IsOwnerSubject(auth.ZulipSubject(202)) {
			t.Fatal("zulip owner subject not registered")
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "You are now registered as the bot owner.") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("invalid token rejected", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		h := New(owners, nil, nil, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "owner=wrong-token",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if owners.HasOwner() {
			t.Fatal("owner should not be registered")
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Invalid authentication token") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("activation error included in response", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		activator := &fakeOwnerActivator{err: errors.New("boot failed")}
		h := New(owners, nil, nil, nil, activator, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "owner=secret-123",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Could not start owner session") {
			t.Fatalf("last text = %q, want session start failure warning", text)
		}
	})
}

func TestStartHandler_ExistingOwner(t *testing.T) {
	t.Run("existing owner running start gets already registered message", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		activator := &fakeOwnerActivator{}
		h := New(owners, nil, nil, nil, activator, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if len(activator.activated) != 1 {
			t.Fatalf("activator calls = %d, want 1", len(activator.activated))
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "already registered as the bot owner") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("existing owner running invite does not consume invite", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		invites := &fakeInviteStore{invites: map[string]*auth.Invite{
			"tok-1": {CreatedBy: "admin"},
		}}
		collaborators := newFakeCollaboratorStore()
		h := New(owners, invites, collaborators, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "invite=tok-1",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "You are already the bot owner") {
			t.Fatalf("last text = %q", text)
		}
		if len(collaborators.collaborators) != 0 {
			t.Fatalf("collaborator should not be added for owner")
		}
	})

	t.Run("non-owner rejected when owner exists", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		h := New(owners, nil, nil, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Transport:    "telegram",
			Principal:    "999",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Bot owner is already registered") {
			t.Fatalf("last text = %q", text)
		}
	})
}

func TestStartHandler_InviteFlow(t *testing.T) {
	t.Run("valid invite adds collaborator", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		invites := &fakeInviteStore{invites: map[string]*auth.Invite{
			"valid-token": {CreatedBy: "101"},
		}}
		collaborators := newFakeCollaboratorStore()
		h := New(owners, invites, collaborators, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "invite=valid-token",
			Transport:    "telegram",
			Principal:    "303",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if _, ok := collaborators.collaborators["303"]; !ok {
			t.Fatal("collaborator 303 was not added")
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "Welcome! You are now a bot collaborator.") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("expired invite rejected", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		invites := &fakeInviteStore{invites: map[string]*auth.Invite{}}
		collaborators := newFakeCollaboratorStore()
		h := New(owners, invites, collaborators, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "invite=expired-token",
			Transport:    "telegram",
			Principal:    "303",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "invalid or has expired") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("existing collaborator gets already collaborator response", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		_, _ = owners.RegisterOwner(101, 9001)
		collaborators := newFakeCollaboratorStore()
		collaborators.collaborators["303"] = &auth.Collaborator{UserID: "303"}
		h := New(owners, nil, collaborators, nil, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "",
			Transport:    "telegram",
			Principal:    "303",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "already a bot collaborator") {
			t.Fatalf("last text = %q", text)
		}
	})
}

func TestStartHandler_ChannelTokenFlow(t *testing.T) {
	t.Run("valid channel token connects account", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		owners.hasOwner = true
		activator := &fakeOwnerActivator{}
		channelAuth := &fakeChannelAuthService{
			consumedTokens: map[string]bool{"balda_testtoken123": true},
		}
		h := New(owners, nil, nil, channelAuth, activator, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "balda_testtoken123",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if len(activator.activated) != 1 {
			t.Fatalf("activator calls = %d, want 1", len(activator.activated))
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "connected to the Balda owner") {
			t.Fatalf("last text = %q", text)
		}
	})

	t.Run("invalid channel token rejected", func(t *testing.T) {
		dispatcher := &recordingDispatcher{}
		owners := newFakeOwnerStore()
		owners.hasOwner = true
		channelAuth := &fakeChannelAuthService{
			consumedTokens: map[string]bool{},
		}
		h := New(owners, nil, nil, channelAuth, nil, dispatcher, "secret-123", zerolog.Nop())
		env := actorlayer.Envelope{ID: "env-1"}
		payload := commandcmd.Payload{
			Name:         "start",
			Args:         "balda_invalidtoken12",
			Transport:    "telegram",
			Principal:    "101",
			Conversation: commandcmd.Conversation{Direct: true},
			Locator:      telegramLocator(9001),
		}
		if err := h.Handle(context.Background(), env, payload); err != nil {
			t.Fatal(err)
		}
		if text := dispatcher.lastText(t); !strings.Contains(text, "invalid or has expired") {
			t.Fatalf("last text = %q", text)
		}
	})
}
