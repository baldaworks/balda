package slackagentfx

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

func TestInboundProcessorUsesExistingThreadSessionAndPublishesTurn(t *testing.T) {
	t.Parallel()
	locator := slackagent.NewThreadLocator("T123", "D456", "1782234671.392669")
	topic := &baldasession.TopicSession{}
	setPrivateField(t, topic, "sessionID", locator.SessionID)
	setPrivateField(t, topic, "userID", "slackagent:T123:U456")
	setPrivateField(t, topic, "agentSessionID", "agent-session-1")
	manager := &baldasession.Manager{}
	setPrivateField(t, manager, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: topic})
	setPrivateField(t, manager, "sessionStore", restoreStoreStub{})
	dispatcher := &dispatcherRecorder{}
	lifecycle := &sessionLifecycleStub{}
	processor := newInboundProcessor(inboundProcessorParams{
		SessionManager: manager,
		Dispatcher:     dispatcher,
		Lifecycle:      lifecycle,
		Logger:         zerolog.Nop(),
	})
	envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{
		Type: "event_callback",
		Event: slackagent.Event{
			EventID:     "Ev123",
			EventType:   "message",
			UserID:      "U456",
			Text:        "hello",
			ChannelType: "im",
			Conversation: slackagent.ConversationRef{
				TeamID:         "T123",
				ConversationID: "D456",
				ThreadID:       "1782234671.392669",
			},
			Message: &slackagent.MessageRef{MessageID: "1782234987.693923", ThreadTS: "1782234671.392669"},
		},
	}, testTimestamp())
	if err != nil {
		t.Fatalf("BuildIngressEnvelope() error = %v", err)
	}

	settlement, err := processor.ProcessInbound(context.Background(), envelope)
	if err != nil {
		t.Fatalf("ProcessInbound() error = %v", err)
	}
	if settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("settlement = %+v, want accepted", settlement)
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(dispatcher.commands))
	}
	if len(lifecycle.begins) != 1 || lifecycle.begins[0].initiator != "U456" || lifecycle.begins[0].prompt != "hello" {
		t.Fatalf("lifecycle begins = %+v", lifecycle.begins)
	}
	command := dispatcher.commands[0]
	if command.To.Target != baldaexecution.ActorTypeSession || command.DedupeKey != "slackagent:Ev123" {
		t.Fatalf("command = %+v", command)
	}
	var payload actors.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(command.Payload, &payload); err != nil {
		t.Fatalf("decode session turn: %v", err)
	}
	if payload.UserID != "slackagent:T123:U456" || payload.Source != "slackagent" || !payload.Deliver {
		t.Fatalf("payload = %+v", payload)
	}
}

func TestTurnCancellerCancelsQueuedTurnThenReconcilesSlackSession(t *testing.T) {
	t.Parallel()
	queue := &turnQueueStub{hadInFlight: true, dropped: 2}
	lifecycle := &sessionLifecycleStub{}
	canceller := newTurnCanceller(controlapp.New(queue, nil, nil, nil, nil, zerolog.Nop()), lifecycle)
	locator := slackagent.NewThreadLocator("T123", "D456", "root-ts")
	err := canceller.CancelTurn(context.Background(), slackagent.SessionStopped{
		Locator:     locator,
		RequestedBy: "slackagent:T123:U456",
	})
	if err != nil {
		t.Fatalf("CancelTurn() error = %v", err)
	}
	if queue.calls != 1 || !queue.clearQueued || queue.locator.SessionID != locator.SessionID {
		t.Fatalf("queue = %+v", queue)
	}
	if len(lifecycle.stopped) != 1 || lifecycle.stopped[0].SessionID != locator.SessionID {
		t.Fatalf("stopped lifecycle = %+v", lifecycle.stopped)
	}
}

func TestBoundaryObserverClosesOnlySlackCloseBoundary(t *testing.T) {
	t.Parallel()
	lifecycle := &sessionLifecycleStub{}
	observer := newBoundaryObserver(lifecycle)
	slackLocator := slackagent.NewThreadLocator("T123", "D456", "root-ts")
	if err := observer.BeforeSessionBoundary(context.Background(), baldasession.SessionBoundary{
		Locator: slackLocator,
		Reason:  baldasession.BoundaryReasonReset,
	}); err != nil {
		t.Fatalf("reset boundary error = %v", err)
	}
	if err := observer.BeforeSessionBoundary(context.Background(), baldasession.SessionBoundary{
		Locator: baldasession.SessionLocator{ChannelType: "telegram", SessionID: "tg-1"},
		Reason:  baldasession.BoundaryReasonClose,
	}); err != nil {
		t.Fatalf("non-slack boundary error = %v", err)
	}
	if err := observer.BeforeSessionBoundary(context.Background(), baldasession.SessionBoundary{
		Locator: slackLocator,
		Reason:  baldasession.BoundaryReasonClose,
	}); err != nil {
		t.Fatalf("close boundary error = %v", err)
	}
	if len(lifecycle.closed) != 1 || lifecycle.closed[0].SessionID != slackLocator.SessionID {
		t.Fatalf("closed lifecycle = %+v", lifecycle.closed)
	}
}

func testTimestamp() time.Time {
	return time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
}

type dispatcherRecorder struct {
	commands []actorlayer.Envelope
}

type turnQueueStub struct {
	calls       int
	locator     baldasession.SessionLocator
	clearQueued bool
	hadInFlight bool
	dropped     int
}

func (q *turnQueueStub) Enqueue(context.Context, appports.TurnTask) (<-chan error, int, error) {
	return nil, 0, nil
}

func (q *turnQueueStub) CancelSession(locator baldasession.SessionLocator, clearQueued bool) (bool, int, error) {
	q.calls++
	q.locator = locator
	q.clearQueued = clearQueued
	return q.hadInFlight, q.dropped, nil
}

func (d *dispatcherRecorder) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.commands = append(d.commands, envelope)
	return &actortransport.DispatchReceipt{}, nil
}

func (d *dispatcherRecorder) PublishEvent(context.Context, string, actorlayer.Envelope) error {
	return nil
}

func setPrivateField[T any](t *testing.T, target any, name string, value T) {
	t.Helper()
	field := reflect.ValueOf(target).Elem().FieldByName(name)
	reflect.NewAt(field.Type(), unsafe.Pointer(field.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

type restoreStoreStub struct{}

func (restoreStoreStub) Upsert(context.Context, state.SessionRecord) error { return nil }
func (restoreStoreStub) GetByAddress(context.Context, string, string) (state.SessionRecord, bool, error) {
	return state.SessionRecord{}, false, nil
}
func (restoreStoreStub) GetBySessionID(context.Context, string) (state.SessionRecord, bool, error) {
	return state.SessionRecord{}, false, nil
}
func (restoreStoreStub) DeleteBySessionID(context.Context, string) error     { return nil }
func (restoreStoreStub) List(context.Context) ([]state.SessionRecord, error) { return nil, nil }

type beginCall struct {
	locator   baldasession.SessionLocator
	initiator string
	prompt    string
}

type sessionLifecycleStub struct {
	begins  []beginCall
	stopped []baldasession.SessionLocator
	closed  []baldasession.SessionLocator
	err     error
}

func (s *sessionLifecycleStub) BeginTurn(_ context.Context, locator baldasession.SessionLocator, initiator, prompt string) error {
	s.begins = append(s.begins, beginCall{locator: locator, initiator: initiator, prompt: prompt})
	return s.err
}

func (s *sessionLifecycleStub) HandleSessionStopped(_ context.Context, locator baldasession.SessionLocator) error {
	s.stopped = append(s.stopped, locator)
	return s.err
}

func (s *sessionLifecycleStub) CloseSession(_ context.Context, locator baldasession.SessionLocator) error {
	s.closed = append(s.closed, locator)
	return s.err
}
