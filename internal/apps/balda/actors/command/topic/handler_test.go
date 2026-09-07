package topic

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type fakeTopicSessions struct {
	createdSession []session.SessionContext
	createdAgent   string
	providerID     string
	createErr      error
}

func (s *fakeTopicSessions) CreateSession(_ context.Context, sessionCtx session.SessionContext, agentName string) error {
	if s.createErr != nil {
		return s.createErr
	}
	s.createdSession = append(s.createdSession, sessionCtx)
	s.createdAgent = agentName
	return nil
}

func (s *fakeTopicSessions) BaldaProviderID() string {
	return s.providerID
}

func (s *fakeTopicSessions) GetAgentMetadata(agentName string) session.AgentMetadata {
	return session.AgentMetadata{
		Type:       "test-agent",
		Model:      "test-model",
		MCPServers: []string{"server1"},
	}
}

type fakeTopicCreator struct {
	createdTopicName string
	createdLocator   deliverycmd.Locator
	createErr        error
	closedLocators   []deliverycmd.Locator
}

func (c *fakeTopicCreator) CreateTopic(_ context.Context, _ deliverycmd.Locator, topicName string) (deliverycmd.Locator, error) {
	if c.createErr != nil {
		return deliverycmd.Locator{}, c.createErr
	}
	c.createdTopicName = topicName
	return c.createdLocator, nil
}

func (c *fakeTopicCreator) CloseTopic(_ context.Context, locator deliverycmd.Locator) error {
	c.closedLocators = append(c.closedLocators, locator)
	return nil
}

type recordingTopicDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (r *recordingTopicDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	r.dispatched = append(r.dispatched, env)
	return &actortransport.DispatchReceipt{}, nil
}

func lastDispatchedText(t *testing.T, rd *recordingTopicDispatcher) string {
	t.Helper()
	if len(rd.dispatched) == 0 {
		t.Fatalf("no envelopes dispatched")
	}
	env := rd.dispatched[len(rd.dispatched)-1]
	var payload actors.DeliveryPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("failed to unmarshal delivery payload: %v", err)
	}
	return payload.Text
}

func TestTopicHandler_Unauthorized(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	creator := &fakeTopicCreator{}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:   commandcmd.SchemaVersion,
		Name:      "topic",
		Args:      "alpha",
		Transport: "telegram",
		Locator:   deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal: "tg-1",
		Access:    commandcmd.Access{SessionCommands: false},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Only the bot owner or collaborators can use this command.") {
		t.Errorf("got %q, want permission denied", got)
	}
}

func TestTopicHandler_TelegramGroupChat_Rejects(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	creator := &fakeTopicCreator{}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "alpha",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg--100-0", AddressKey: "-100:0"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: false},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-2"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "This command is only available in direct messages.") {
		t.Errorf("got %q, want DM only message", got)
	}
}

func TestTopicHandler_ZulipDM_Rejects(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	creator := &fakeTopicCreator{}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "alpha",
		Transport:    "zulip",
		Locator:      deliverycmd.Locator{ChannelType: "zulip", SessionID: "zu-dm-10", AddressKey: "dm:10"},
		Principal:    "zu-10",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-3"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "This command is only available in stream messages.") {
		t.Errorf("got %q, want stream only message", got)
	}
}

func TestTopicHandler_EmptyArgs_ShowsUsage(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	creator := &fakeTopicCreator{}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-4"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Usage: /topic <name>") {
		t.Errorf("got %q, want usage", got)
	}
}

func TestTopicHandler_ProviderNotReady(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: ""}
	creator := &fakeTopicCreator{}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "alpha",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-5"}, payload); err != nil {
		t.Fatal(err)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Balda is not ready right now.") {
		t.Errorf("got %q, want Balda not ready", got)
	}
}

func TestTopicHandler_CreateSessionFailure_RollsBackChannelTopic(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider", createErr: errors.New("db error")}
	topicLocator := deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-42", AddressKey: "1:42"}
	creator := &fakeTopicCreator{createdLocator: topicLocator}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "alpha",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal:    "tg-1",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-6"}, payload); err != nil {
		t.Fatal(err)
	}
	if len(creator.closedLocators) != 1 || creator.closedLocators[0].SessionID != "tg-1-42" {
		t.Errorf("expected rollback CloseTopic call for tg-1-42, got %+v", creator.closedLocators)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "Could not create topic session.") {
		t.Errorf("got %q, want Could not create topic session", got)
	}
}

func TestTopicHandler_TelegramSuccess(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	topicLocator := deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-42", AddressKey: "1:42"}
	creator := &fakeTopicCreator{createdLocator: topicLocator}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "ops run",
		Transport:    "telegram",
		Locator:      deliverycmd.Locator{ChannelType: "telegram", SessionID: "tg-1-0", AddressKey: "1:0"},
		Principal:    "tg-101",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-7"}, payload); err != nil {
		t.Fatal(err)
	}
	if creator.createdTopicName != "ops run" {
		t.Errorf("createdTopicName = %q, want ops run", creator.createdTopicName)
	}
	if len(sessions.createdSession) != 1 || sessions.createdAgent != "ops run" || sessions.createdSession[0].UserID != "tg-101" {
		t.Errorf("created session = %+v, agent = %q", sessions.createdSession, sessions.createdAgent)
	}
	got := lastDispatchedText(t, dispatcher)
	if !strings.Contains(got, "ops run") || !strings.Contains(got, "tg-1-42") {
		t.Errorf("welcome message = %q, want mention of ops run and tg-1-42", got)
	}
}

func TestTopicHandler_ZulipSuccess(t *testing.T) {
	sessions := &fakeTopicSessions{providerID: "test-provider"}
	topicLocator := deliverycmd.Locator{ChannelType: "zulip", SessionID: "zu-s-5-1234", AddressKey: "s:5:ops-run"}
	creator := &fakeTopicCreator{createdLocator: topicLocator}
	dispatcher := &recordingTopicDispatcher{}
	h := New(sessions, creator, dispatcher, zerolog.Nop())

	origin := deliverycmd.Locator{ChannelType: "zulip", SessionID: "zu-s-5-0000", AddressKey: "s:5:general"}
	payload := commandcmd.Payload{
		Version:      commandcmd.SchemaVersion,
		Name:         "topic",
		Args:         "ops-run",
		Transport:    "zulip",
		Locator:      origin,
		Principal:    "zu-101",
		Access:       commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: false},
	}
	if err := h.Handle(context.Background(), actorlayer.Envelope{ID: "op-8"}, payload); err != nil {
		t.Fatal(err)
	}
	if len(sessions.createdSession) != 1 || sessions.createdAgent != "ops-run" {
		t.Errorf("created session = %+v", sessions.createdSession)
	}
	// Should have dispatched 2 envelopes: welcome to new topic, then confirmation to origin
	if len(dispatcher.dispatched) != 2 {
		t.Fatalf("dispatched envelopes = %d, want 2", len(dispatcher.dispatched))
	}
	confirmation := lastDispatchedText(t, dispatcher)
	if !strings.Contains(confirmation, "Session created. Post in topic 'ops-run' to continue.") {
		t.Errorf("confirmation = %q", confirmation)
	}
}
