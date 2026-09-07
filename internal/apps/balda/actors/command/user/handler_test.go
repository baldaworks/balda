package user

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeDispatcher struct {
	sentEnvelopes []actorlayer.Envelope
}

func (d *fakeDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.sentEnvelopes = append(d.sentEnvelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func (d *fakeDispatcher) lastText(t *testing.T) string {
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

type fakeOwnerStore struct {
	ownerID  int64
	subjects map[string]bool
}

func (f *fakeOwnerStore) IsOwner(userID int64) bool {
	return f.ownerID != 0 && f.ownerID == userID
}

func (f *fakeOwnerStore) IsOwnerSubject(subject string) bool {
	return f.subjects[subject]
}

type fakeInviteStore struct {
	invites    []auth.Invite
	token      string
	createErr  error
	listErr    error
	createdFor string
}

func (f *fakeInviteStore) CreateInvite(_ context.Context, createdBy string) (string, *auth.Invite, error) {
	f.createdFor = createdBy
	if f.createErr != nil {
		return "", nil, f.createErr
	}
	return f.token, &auth.Invite{CreatedBy: createdBy, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour)}, nil
}

func (f *fakeInviteStore) ListInvites(_ context.Context) ([]auth.Invite, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.invites, nil
}

type fakeCollaboratorStore struct {
	collaborators []auth.Collaborator
	listErr       error
	removeErr     error
	removedID     string
}

func (f *fakeCollaboratorStore) ListCollaborators(_ context.Context) ([]auth.Collaborator, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.collaborators, nil
}

func (f *fakeCollaboratorStore) RemoveCollaborator(_ context.Context, userID string) error {
	f.removedID = userID
	if f.removeErr != nil {
		return f.removeErr
	}
	return nil
}

type fakeBotUsernameProvider struct {
	username string
}

func (f *fakeBotUsernameProvider) GetBotUsername(_ context.Context) string {
	return f.username
}

func newPayload(transport, principal, args string, isOwner bool) (actorlayer.Envelope, commandcmd.Payload) {
	loc, _ := deliverycmd.NewLocator(transport, "chat-1", "{}", "sess-1")
	p := commandcmd.Payload{
		Version:   commandcmd.SchemaVersion,
		Name:      "user",
		Args:      args,
		Locator:   loc,
		Transport: transport,
		Principal: principal,
		Access:    commandcmd.Access{Owner: isOwner},
	}
	env := actorlayer.Envelope{
		ID: "test-envelope-id",
	}
	return env, p
}

func TestHandler_NotOwner(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	handler := New(
		&fakeOwnerStore{ownerID: 999},
		&fakeInviteStore{},
		&fakeCollaboratorStore{},
		nil,
		dispatcher,
		zerolog.Nop(),
	)

	env, p := newPayload(transportTelegram, "100", "add", false)
	if err := handler.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle() unexpected error: %v", err)
	}

	got := dispatcher.lastText(t)
	if got != "This command is only for the owner." {
		t.Fatalf("expected not-owner message, got: %q", got)
	}
}

func TestHandler_OwnerFromAccess(t *testing.T) {
	dispatcher := &fakeDispatcher{}
	inviteStore := &fakeInviteStore{token: "tok-123"}
	handler := New(
		nil, // no ownerStore, rely on Access.Owner
		inviteStore,
		&fakeCollaboratorStore{},
		&fakeBotUsernameProvider{username: "mybot"},
		dispatcher,
		zerolog.Nop(),
	)

	env, p := newPayload(transportTelegram, "100", "add", true)
	if err := handler.Handle(context.Background(), env, p); err != nil {
		t.Fatalf("Handle() unexpected error: %v", err)
	}

	got := dispatcher.lastText(t)
	if !strings.Contains(got, "https://t.me/mybot?start=invite_tok-123") {
		t.Fatalf("expected invite link with bot username, got: %q", got)
	}
}

func TestHandler_Usage(t *testing.T) {
	t.Run("telegram usage", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		handler := New(nil, nil, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "100", "", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "• /user add - Generate invite link") {
			t.Fatalf("expected telegram usage with invite link, got: %q", got)
		}
	})

	t.Run("zulip usage", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		handler := New(nil, nil, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportZulip, "100", "unknown_subcmd", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "• /user add - Generate invite token") {
			t.Fatalf("expected zulip usage with invite token, got: %q", got)
		}
	})
}

func TestHandler_Add(t *testing.T) {
	t.Run("telegram add success with bot username fallback", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		inviteStore := &fakeInviteStore{token: "abc-token"}
		handler := New(nil, inviteStore, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "add", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "https://t.me/<bot_username>?start=invite_abc-token") {
			t.Fatalf("expected invite link with default placeholder, got: %q", got)
		}
		if inviteStore.createdFor != "555" {
			t.Fatalf("expected invite created for 555, got: %q", inviteStore.createdFor)
		}
	})

	t.Run("zulip add success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		inviteStore := &fakeInviteStore{token: "zulip-tok"}
		handler := New(nil, inviteStore, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportZulip, "777", "invite", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "Invite token created:\nzulip-tok") || !strings.Contains(got, "/start invite=zulip-tok") {
			t.Fatalf("expected zulip invite token message, got: %q", got)
		}
	})

	t.Run("telegram add error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		inviteStore := &fakeInviteStore{createErr: errors.New("kv failed")}
		handler := New(nil, inviteStore, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "add", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Failed to create invite. Please try again." {
			t.Fatalf("expected telegram error text, got: %q", got)
		}
	})

	t.Run("zulip add error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		inviteStore := &fakeInviteStore{createErr: errors.New("kv failed")}
		handler := New(nil, inviteStore, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportZulip, "555", "add", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Failed to create invite token." {
			t.Fatalf("expected zulip error text, got: %q", got)
		}
	})
}

func TestHandler_List(t *testing.T) {
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	collaborators := []auth.Collaborator{
		{UserID: "collab-1", Username: "alice", AddedAt: now},
		{UserID: "collab-2", FirstName: "Bob", AddedAt: now},
	}
	invites := []auth.Invite{
		{CreatedBy: "555", ExpiresAt: now.Add(24 * time.Hour)},
	}

	t.Run("telegram list with collaborators and invites", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{collaborators: collaborators}
		inviteStore := &fakeInviteStore{invites: invites}
		handler := New(nil, inviteStore, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "• collab-1 (@alice) - added 2026-09-07 12:00") {
			t.Fatalf("expected telegram collaborator with @alice, got: %q", got)
		}
		if !strings.Contains(got, "• collab-2 (Bob) - added 2026-09-07 12:00") {
			t.Fatalf("expected telegram collaborator with Bob, got: %q", got)
		}
		if !strings.Contains(got, "Active Invites:\nexpires 2026-09-08 12:00") {
			t.Fatalf("expected active invites, got: %q", got)
		}
	})

	t.Run("zulip list with collaborators and invites", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{collaborators: collaborators}
		inviteStore := &fakeInviteStore{invites: invites}
		handler := New(nil, inviteStore, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportZulip, "555", "list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if !strings.Contains(got, "• collab-1 (alice) - added 2026-09-07 12:00") {
			t.Fatalf("expected zulip collaborator without @, got: %q", got)
		}
	})

	t.Run("empty collaborators and invites", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{collaborators: nil}
		inviteStore := &fakeInviteStore{invites: nil}
		handler := New(nil, inviteStore, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "No collaborators" {
			t.Fatalf("expected 'No collaborators', got: %q", got)
		}
	})

	t.Run("collaborator list error", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{listErr: errors.New("db err")}
		inviteStore := &fakeInviteStore{}
		handler := New(nil, inviteStore, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "list", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Failed to list collaborators. Please try again." {
			t.Fatalf("expected telegram error message, got: %q", got)
		}
	})
}

func TestHandler_Remove(t *testing.T) {
	t.Run("remove success", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{}
		handler := New(nil, nil, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "remove user-42", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Collaborator removed: user-42" {
			t.Fatalf("expected removed text, got: %q", got)
		}
		if collabStore.removedID != "user-42" {
			t.Fatalf("expected removedID 'user-42', got: %q", collabStore.removedID)
		}
	})

	t.Run("remove missing user_id argument", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		handler := New(nil, nil, nil, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "remove", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Usage: /user remove <user_id>" {
			t.Fatalf("expected usage text, got: %q", got)
		}
	})

	t.Run("remove error telegram", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{removeErr: errors.New("db fail")}
		handler := New(nil, nil, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportTelegram, "555", "remove user-42", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Could not remove collaborator. Please try again." {
			t.Fatalf("expected telegram error text, got: %q", got)
		}
	})

	t.Run("remove error zulip", func(t *testing.T) {
		dispatcher := &fakeDispatcher{}
		collabStore := &fakeCollaboratorStore{removeErr: errors.New("db fail")}
		handler := New(nil, nil, collabStore, nil, dispatcher, zerolog.Nop())

		env, p := newPayload(transportZulip, "555", "remove user-42", true)
		if err := handler.Handle(context.Background(), env, p); err != nil {
			t.Fatal(err)
		}

		got := dispatcher.lastText(t)
		if got != "Could not remove collaborator." {
			t.Fatalf("expected zulip error text, got: %q", got)
		}
	})
}
