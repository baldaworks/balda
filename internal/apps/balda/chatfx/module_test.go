package chatfx

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
	adksession "google.golang.org/adk/v2/session"
)

type fakeSessionStore struct {
	record baldastate.SessionRecord
	found  bool
}

func (f *fakeSessionStore) Upsert(_ context.Context, record baldastate.SessionRecord) error {
	f.record = record
	f.found = true
	return nil
}

func (f *fakeSessionStore) GetByAddress(_ context.Context, _, _ string) (baldastate.SessionRecord, bool, error) {
	if !f.found {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (f *fakeSessionStore) GetBySessionID(_ context.Context, sessionID string) (baldastate.SessionRecord, bool, error) {
	if !f.found || f.record.SessionID != sessionID {
		return baldastate.SessionRecord{}, false, nil
	}
	return f.record, true, nil
}

func (*fakeSessionStore) DeleteBySessionID(context.Context, string) error { return nil }
func (f *fakeSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	if !f.found {
		return nil, nil
	}
	return []baldastate.SessionRecord{f.record}, nil
}

type fakeAgentBuilder struct {
	metadata baldasession.AgentMetadata
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
	return nil, nil
}

func (f *fakeAgentBuilder) GetAgentMetadata(string) baldasession.AgentMetadata { return f.metadata }

type fakeRuntimeManager struct{}

func (*fakeRuntimeManager) Runtime(context.Context) (*baldasession.BuiltRuntime, error) {
	return &baldasession.BuiltRuntime{}, nil
}

func (f *fakeRuntimeManager) ProviderID() string { return "test-provider" }

func newTestSessionManager(t *testing.T) *baldasession.Manager {
	t.Helper()
	mgr, err := baldasession.NewManager(baldasession.ManagerParams{
		AgentBuilder: &fakeAgentBuilder{
			metadata: baldasession.AgentMetadata{
				Type:  "test",
				Model: "test",
			},
		},
		RuntimeManager:  &fakeRuntimeManager{},
		BaldaProviderID: "test-provider",
		SessionStore:    &fakeSessionStore{},
		Logger:          zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("NewManager() error: %v", err)
	}
	return mgr
}

type fakeDispatcher struct {
	dispatched []actorlayer.Envelope
}

func (f *fakeDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.dispatched = append(f.dispatched, env)
	return &actortransport.DispatchReceipt{MsgID: env.ID}, nil
}

func TestSessionAdapter_Prepare_CreatesSessionWhenNotFound(t *testing.T) {
	mgr := newTestSessionManager(t)
	adapter := NewSessionAdapter(mgr)

	prep, err := adapter.Prepare(context.Background(), chatapp.InboundContext{
		InboundID:   "in-1",
		ChannelType: "telegram",
		AddressKey:  "9001:0",
		SessionID:   "s-auto-1",
		UserID:      "user-1",
	})
	if err != nil {
		t.Fatalf("Prepare() error: %v", err)
	}
	if !prep.Ready {
		t.Fatal("expected Ready = true")
	}
	if prep.UserID != "user-1" {
		t.Fatalf("prep.UserID = %q, want user-1", prep.UserID)
	}
}

func TestSessionAdapter_Prepare_NilManagerFails(t *testing.T) {
	adapter := NewSessionAdapter(nil)
	_, err := adapter.Prepare(context.Background(), chatapp.InboundContext{})
	if err == nil {
		t.Fatal("expected error for nil session manager")
	}
}

func TestDispatcherAdapter_Dispatch(t *testing.T) {
	f := &fakeDispatcher{}
	adapter := NewDispatcherAdapter(f)

	receipt, err := adapter.Dispatch(context.Background(), actorlayer.Envelope{ID: "e-1"})
	if err != nil {
		t.Fatalf("Dispatch() error: %v", err)
	}
	if receipt == nil || receipt.MsgID != "e-1" {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}

	nilAdapter := NewDispatcherAdapter(nil)
	_, err = nilAdapter.Dispatch(context.Background(), actorlayer.Envelope{})
	if err == nil {
		t.Fatal("expected error for nil dispatcher")
	}
}

func TestQuestionAdapter_NilSafe(t *testing.T) {
	adapter := NewQuestionAdapter(nil)
	res, err := adapter.ResolveQuestionReply(context.Background(), questioncmd.InboundReply{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Matched {
		t.Fatal("expected Matched = false")
	}
}

func TestModule_ProvidesChatHandler(t *testing.T) {
	var handler chatapp.Handler
	app := fx.New(
		fx.NopLogger,
		fx.Provide(
			func() *baldasession.Manager { return newTestSessionManager(t) },
			func() actortransport.Dispatcher { return &fakeDispatcher{} },
			zerolog.Nop,
		),
		Module,
		fx.Populate(&handler),
	)

	if err := app.Start(context.Background()); err != nil {
		t.Fatalf("app.Start failed: %v", err)
	}
	defer func() { _ = app.Stop(context.Background()) }()

	if handler == nil {
		t.Fatal("chatapp.Handler was not populated")
	}
}
