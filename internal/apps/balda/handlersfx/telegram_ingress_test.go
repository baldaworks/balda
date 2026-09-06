package handlersfx

import (
	"context"
	"strings"
	"sync"
	"testing"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
	"github.com/tgbotkit/runtime/messagetype"
	adksession "google.golang.org/adk/v2/session"
)

type fakeTurnDispatcher struct {
	commandsMu sync.Mutex
	commands   []actorlayer.Envelope
}

func (f *fakeTurnDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.commandsMu.Lock()
	defer f.commandsMu.Unlock()
	f.commands = append(f.commands, env)
	return &actortransport.DispatchReceipt{
		MsgID: env.ID,
	}, nil
}

type fakeSessionStore struct {
	mu              sync.Mutex
	record          baldastate.SessionRecord
	foundByAddress  bool
	lastUpsert      baldastate.SessionRecord
	getByAddressErr error
}

func (f *fakeSessionStore) Upsert(_ context.Context, record baldastate.SessionRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lastUpsert = record
	f.record = record
	f.foundByAddress = true
	return nil
}

func (f *fakeSessionStore) GetByAddress(_ context.Context, channelType, addressKey string) (baldastate.SessionRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.getByAddressErr != nil {
		return baldastate.SessionRecord{}, false, f.getByAddressErr
	}
	if !f.foundByAddress {
		return baldastate.SessionRecord{}, false, nil
	}
	if f.record.ChannelType != channelType || f.record.AddressKey != addressKey {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (f *fakeSessionStore) GetBySessionID(_ context.Context, sessionID string) (baldastate.SessionRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.foundByAddress || f.record.SessionID != sessionID {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (*fakeSessionStore) DeleteBySessionID(context.Context, string) error { return nil }
func (f *fakeSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.foundByAddress {
		return nil, nil
	}
	return []baldastate.SessionRecord{f.record}, nil
}

type fakeAgentBuilder struct {
	metadata baldasession.AgentMetadata
	err      error
}

func (f *fakeAgentBuilder) CreateRuntimeSession(
	context.Context,
	*baldasession.BuiltRuntime,
	string,
	string,
	string,
	string,
	baldasession.RuntimeSessionContext,
) (adksession.Session, error) {
	return nil, f.err
}

func (f *fakeAgentBuilder) GetAgentMetadata(string) baldasession.AgentMetadata { return f.metadata }

type fakeRuntimeManager struct{ providerID string }

func (*fakeRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return &baldasession.BuiltRuntime{}, nil
}

func (f *fakeRuntimeManager) ProviderID() string { return f.providerID }

type fakeOwnerKVStore struct {
	mu    sync.Mutex
	value any
	ok    bool
	err   error
}

func (s *fakeOwnerKVStore) GetJSON(_ context.Context, _ string) (any, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, false, s.err
	}
	return s.value, s.ok, nil
}

func (s *fakeOwnerKVStore) SetJSON(_ context.Context, _ string, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.value = value
	s.ok = true
	return nil
}

type fakeTestChannel struct {
	answerCalls []struct {
		queryID   string
		text      string
		showAlert bool
	}
}

func (c *fakeTestChannel) MessageContextFromEvent(event *events.MessageEvent) (baldatelegram.MessageContext, bool) {
	if event == nil || event.Message == nil {
		return baldatelegram.MessageContext{}, false
	}
	text := ""
	if event.Message.Text != nil {
		text = *event.Message.Text
	}
	return baldatelegram.MessageContext{
		Locator:   baldatelegram.NewLocator(event.Message.Chat.Id, 0),
		ChatID:    event.Message.Chat.Id,
		MessageID: event.Message.MessageId,
		UserID:    event.Message.From.Id,
		Text:      text,
		IsDM:      event.Message.Chat.Type == "private",
	}, true
}

func (c *fakeTestChannel) CommandContextFromEvent(*events.CommandEvent) (baldatelegram.CommandContext, bool) {
	return baldatelegram.CommandContext{}, false
}

func (c *fakeTestChannel) TopicLifecycleFromEvent(event *events.MessageEvent) (baldatelegram.TopicLifecycleContext, bool) {
	if event == nil || event.Message == nil || event.Message.MessageThreadId == nil {
		return baldatelegram.TopicLifecycleContext{}, false
	}
	return baldatelegram.TopicLifecycleContext{
		Locator:   baldatelegram.NewLocator(event.Message.Chat.Id, *event.Message.MessageThreadId),
		ChatID:    event.Message.Chat.Id,
		TopicID:   *event.Message.MessageThreadId,
		MessageID: event.Message.MessageId,
		Type:      event.Type,
	}, true
}

func (c *fakeTestChannel) CallbackContextFromEvent(event *events.CallbackQueryEvent) (baldatelegram.CallbackContext, bool) {
	if event == nil || event.CallbackQuery == nil || event.CallbackQuery.Data == nil {
		return baldatelegram.CallbackContext{}, false
	}
	return baldatelegram.CallbackContext{
		Locator:         baldatelegram.NewLocator(9001, 0),
		CallbackQueryID: event.CallbackQuery.Id,
		QuestionID:      "q1",
		OptionIndex:     1,
		UserID:          event.CallbackQuery.From.Id,
	}, true
}

func (c *fakeTestChannel) CollectMediaGroup(baldatelegram.MessageContext, func(context.Context, baldatelegram.MessageContext)) bool {
	return false
}

func (c *fakeTestChannel) CreateTopicLocator(context.Context, int64, string) (deliverycmd.Locator, error) {
	return deliverycmd.Locator{}, nil
}

func (c *fakeTestChannel) Close(context.Context, deliverycmd.Locator) error { return nil }

func (c *fakeTestChannel) AnswerQuestionCallback(_ context.Context, callbackQueryID, text string, showAlert bool) error {
	c.answerCalls = append(c.answerCalls, struct {
		queryID   string
		text      string
		showAlert bool
	}{queryID: callbackQueryID, text: text, showAlert: showAlert})
	return nil
}

func newTestOwnerStore(t *testing.T) *auth.OwnerStore {
	t.Helper()
	store, err := auth.NewOwnerStore(&fakeOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := store.RegisterOwner(101, 9001); err != nil {
		t.Fatalf("RegisterOwner() error = %v", err)
	}
	return store
}

func newTestSessionManager(t *testing.T, store *fakeSessionStore) *baldasession.Manager {
	t.Helper()
	m, err := baldasession.NewManager(baldasession.ManagerParams{
		AgentBuilder: &fakeAgentBuilder{
			metadata: baldasession.AgentMetadata{
				Type:       "opencode_acp",
				Model:      "gpt-5",
				MCPServers: []string{"provider_mcp"},
			},
		},
		RuntimeManager:  &fakeRuntimeManager{providerID: "alpha"},
		BaldaProviderID: "alpha",
		SessionStore:    store,
		Logger:          zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	return m
}

type testHarness struct {
	handler    *telegramInboundHandler
	server     *baldatelegram.Server
	dispatcher *fakeTurnDispatcher
	channel    *fakeTestChannel
	sessions   *baldasession.Manager
	ownerStore *auth.OwnerStore
}

func newTestHarness(t *testing.T) *testHarness {
	t.Helper()
	ownerStore := newTestOwnerStore(t)
	sessionStore := &fakeSessionStore{}
	sessions := newTestSessionManager(t, sessionStore)
	dispatcher := &fakeTurnDispatcher{}
	ch := &fakeTestChannel{}

	handler := newTelegramInboundHandler(telegramInboundHandlerParams{
		OwnerStore:      ownerStore,
		SessionManager:  sessions,
		Dispatcher:      dispatcher,
		BaldaProviderID: "alpha",
		Logger:          zerolog.Nop(),
	})
	handler.channel = ch
	handler.setOwner(101, 9001)
	handler.botUsername = "testbot"
	handler.botUserID = 4242

	server := baldatelegram.NewServer(baldatelegram.ServerParams{
		Channel:          ch,
		InboundHandler:   handler,
		LifecycleHandler: handler,
		TelegramEnabled:  true,
		Logger:           zerolog.Nop(),
	})

	return &testHarness{
		handler:    handler,
		server:     server,
		dispatcher: dispatcher,
		channel:    ch,
		sessions:   sessions,
		ownerStore: ownerStore,
	}
}

func TestTelegramInboundHandler_HandleMessage_PublishesTurn(t *testing.T) {
	h := newTestHarness(t)

	msgText := "hello balda"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			MessageId: 10,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 101},
			Text:      &msgText,
		},
	}

	if err := h.server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	h.dispatcher.commandsMu.Lock()
	defer h.dispatcher.commandsMu.Unlock()
	if len(h.dispatcher.commands) != 2 {
		t.Fatalf("dispatched commands = %d, want 2 (welcome + turn)", len(h.dispatcher.commands))
	}
	welcomeCmd := h.dispatcher.commands[0]
	if welcomeCmd.To.Target != "delivery" || welcomeCmd.To.Key != "telegram:9001:0" {
		t.Fatalf("welcomeCmd To = %+v, want delivery:telegram:9001:0", welcomeCmd.To)
	}
	cmd := h.dispatcher.commands[1]
	if cmd.Namespace != baldaexecution.NamespaceHumanInbound {
		t.Fatalf("Namespace = %q, want %q", cmd.Namespace, baldaexecution.NamespaceHumanInbound)
	}
}

func TestTelegramInboundHandler_HandleMessage_IgnoresUnauthorizedUser(t *testing.T) {
	h := newTestHarness(t)

	msgText := "unauthorized hello"
	event := &events.MessageEvent{
		Type: messagetype.Text,
		Message: &client.Message{
			MessageId: 11,
			Chat:      client.Chat{Id: 9001, Type: "private"},
			From:      &client.User{Id: 999}, // not owner or collaborator
			Text:      &msgText,
		},
	}

	if err := h.server.HandleMessage(context.Background(), event); err != nil {
		t.Fatalf("HandleMessage() error = %v", err)
	}

	h.dispatcher.commandsMu.Lock()
	defer h.dispatcher.commandsMu.Unlock()
	if len(h.dispatcher.commands) != 0 {
		t.Fatalf("dispatched commands = %d, want 0 for unauthorized user", len(h.dispatcher.commands))
	}
}

func TestTelegramInboundHandler_HandleForumTopic_ClosedPublishesCancel(t *testing.T) {
	h := newTestHarness(t)
	locator := baldatelegram.NewLocator(9001, 77)
	// Create session first
	_, err := h.sessions.EnsureSession(context.Background(), baldasession.SessionContext{Locator: locator, UserID: "101"}, "topic")
	if err != nil {
		t.Fatalf("EnsureSession() error = %v", err)
	}

	if err := h.handler.HandleForumTopic(context.Background(), baldatelegram.TopicLifecycleContext{
		Locator:   locator,
		ChatID:    9001,
		TopicID:   77,
		MessageID: 42,
		Type:      messagetype.ForumTopicClosed,
	}); err != nil {
		t.Fatalf("HandleForumTopic() error = %v", err)
	}

	h.dispatcher.commandsMu.Lock()
	defer h.dispatcher.commandsMu.Unlock()
	if len(h.dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1 for forum topic close", len(h.dispatcher.commands))
	}
	cmd := h.dispatcher.commands[0]
	if cmd.Namespace != baldaexecution.NamespaceJobControl || cmd.Kind != baldaexecution.KindCancel {
		t.Fatalf("cmd = %+v, want job control cancel", cmd)
	}
}

func TestTelegramInboundHandler_OnBotStarted_RestoresPersistedOwner(t *testing.T) {
	h := newTestHarness(t)

	if err := h.handler.OnBotStarted(context.Background(), 4242, "testbot"); err != nil {
		t.Fatalf("OnBotStarted() error = %v", err)
	}

	ownerID, chatID := h.handler.getOwnerBinding()
	if ownerID != 101 || chatID != 9001 {
		t.Fatalf("owner binding = (%d, %d), want (101, 9001)", ownerID, chatID)
	}
}

func TestTelegramInboundHandler_ActivateOwner(t *testing.T) {
	h := newTestHarness(t)

	if err := h.handler.ActivateOwner(context.Background(), 101, 9001); err != nil {
		t.Fatalf("ActivateOwner() error = %v", err)
	}

	ownerID, chatID := h.handler.getOwnerBinding()
	if ownerID != 101 || chatID != 9001 {
		t.Fatalf("owner binding = (%d, %d), want (101, 9001)", ownerID, chatID)
	}
}

func TestInboundTurnExecutor_SubmitSessionTurn(t *testing.T) {
	dispatcher := &fakeTurnDispatcher{}
	executor := newInboundTurnExecutor(dispatcher)

	receipt, err := executor.SubmitSessionTurn(context.Background(), turncmd.SessionTurnPayload{
		Text: "test",
		Locator: baldasession.SessionLocator{
			ChannelType: "telegram",
			AddressKey:  "9001:0",
			SessionID:   "s-123",
		},
		Source: turncmd.SourceTelegram,
	})
	if err != nil {
		t.Fatalf("SubmitSessionTurn() error = %v", err)
	}
	if receipt == nil {
		t.Fatal("receipt is nil")
	}

	dispatcher.commandsMu.Lock()
	defer dispatcher.commandsMu.Unlock()
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
}

func TestInboundTurnExecutor_SubmitWebhookTask(t *testing.T) {
	dispatcher := &fakeTurnDispatcher{}
	executor := newInboundTurnExecutor(dispatcher)

	receipt, jobID, err := executor.SubmitWebhookTask(context.Background(), turncmd.SessionTurnPayload{
		Text: "webhook task",
		Locator: baldasession.SessionLocator{
			ChannelType: "telegram",
			AddressKey:  "9001:0",
			SessionID:   "s-123",
		},
		Source: turncmd.SourceWebhook,
	}, "route1", "req-123")
	if err != nil {
		t.Fatalf("SubmitWebhookTask() error = %v", err)
	}
	if receipt == nil {
		t.Fatal("receipt is nil")
	}
	if strings.TrimSpace(jobID) == "" {
		t.Fatal("jobID is empty")
	}

	dispatcher.commandsMu.Lock()
	defer dispatcher.commandsMu.Unlock()
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
}
