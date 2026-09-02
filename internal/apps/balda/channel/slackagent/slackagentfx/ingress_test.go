package slackagentfx

import (
	"context"
	"errors"
	"reflect"
	"strings"
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

func TestInboundProcessorHydratesMentionedChannelThreadAndPublishesTurn(t *testing.T) {
	t.Parallel()
	locator := slackagent.NewThreadLocator("T123", "C456", "1782234671.392669")
	topic := &baldasession.TopicSession{}
	setPrivateField(t, topic, "sessionID", locator.SessionID)
	setPrivateField(t, topic, "userID", "slackagent:T123:U456")
	setPrivateField(t, topic, "agentSessionID", "agent-session-1")
	manager := &baldasession.Manager{}
	setPrivateField(t, manager, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: topic})
	setPrivateField(t, manager, "sessionStore", restoreStoreStub{})
	var order []string
	dispatcher := &dispatcherRecorder{onDispatch: func() { order = append(order, "dispatch") }}
	lifecycle := &sessionLifecycleStub{onBegin: func() { order = append(order, "begin") }}
	processor := newInboundProcessor(inboundProcessorParams{
		SessionManager: manager,
		Dispatcher:     dispatcher,
		Lifecycle:      lifecycle,
		History: &threadHistoryStub{snapshot: slackagent.ThreadSnapshot{
			RootTS: "1782234671.392669", CutoffTS: "1782234987.693923", Available: true,
			Messages: []slackagent.ThreadMessage{{TS: "1782234671.392669", AuthorID: "U111", AuthorType: slackagent.ThreadAuthorHuman, Text: "prior discussion"}},
		}},
		Logger: zerolog.Nop(),
	})
	envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{
		Type: "event_callback",
		Event: slackagent.Event{
			EventID:     "Ev123",
			EventType:   "app_mention",
			UserID:      "U456",
			Text:        "<@UBOT> hello",
			ChannelType: "channel",
			Conversation: slackagent.ConversationRef{
				TeamID:         "T123",
				ConversationID: "C456",
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
	if len(lifecycle.begins) != 1 || lifecycle.begins[0].initiator != "U456" || lifecycle.begins[0].prompt != "<@UBOT> hello" {
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
	if !strings.Contains(payload.Text, "prior discussion") || !strings.Contains(payload.Text, "CURRENT_ADDRESSED_REQUEST:\n<@UBOT> hello") {
		t.Fatalf("payload text = %q", payload.Text)
	}
	if !reflect.DeepEqual(order, []string{"dispatch", "begin"}) {
		t.Fatalf("call order = %#v", order)
	}
}

func TestInboundProcessorHistoryFailuresSettleBeforeDispatch(t *testing.T) {
	t.Parallel()
	locator := slackagent.NewThreadLocator("T123", "C456", "100.1")
	envelope := contextualMentionEnvelope(t, "100.1", "100.2")
	tests := []struct {
		name          string
		err           error
		wantRetry     bool
		wantAvailable string
	}{
		{name: "transient", err: &slackagent.APIError{Method: "conversations.replies", StatusCode: 429, Retryable: true}, wantRetry: true},
		{name: "permanent", err: &slackagent.APIError{Method: "conversations.replies", StatusCode: 200, Code: "missing_scope"}, wantAvailable: `"available":false`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &dispatcherRecorder{}
			lifecycle := &sessionLifecycleStub{}
			processor := newInboundProcessor(inboundProcessorParams{
				SessionManager: existingSessionManager(t, locator, "slackagent:T123:U456"),
				Dispatcher:     dispatcher,
				Lifecycle:      lifecycle,
				History:        &threadHistoryStub{err: test.err},
				Logger:         zerolog.Nop(),
			})
			settlement, err := processor.ProcessInbound(context.Background(), envelope)
			if test.wantRetry {
				if err == nil || settlement.Outcome != turncmd.InboundRetry || len(dispatcher.commands) != 0 || len(lifecycle.begins) != 0 {
					t.Fatalf("settlement/error/commands/begins = %+v/%v/%d/%d", settlement, err, len(dispatcher.commands), len(lifecycle.begins))
				}
				return
			}
			if err != nil || settlement.Outcome != turncmd.InboundAccepted || len(dispatcher.commands) != 1 || len(lifecycle.begins) != 1 {
				t.Fatalf("settlement/error/commands/begins = %+v/%v/%d/%d", settlement, err, len(dispatcher.commands), len(lifecycle.begins))
			}
			var payload actors.SessionTurnPayload
			if err := actorlayer.UnmarshalPayload(dispatcher.commands[0].Payload, &payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			if !strings.Contains(payload.Text, test.wantAvailable) || !strings.Contains(payload.Text, `"reason":"missing_scope"`) {
				t.Fatalf("payload text = %q", payload.Text)
			}
		})
	}
}

func TestInboundProcessorSkipsHistoryForDMAndTopLevelMention(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		eventType   string
		channel     string
		channelType string
		rootTS      string
		messageTS   string
	}{
		{name: "dm", eventType: "message", channel: "D456", channelType: "im", rootTS: "100.1", messageTS: "100.1"},
		{name: "top-level mention", eventType: "app_mention", channel: "C456", channelType: "channel", rootTS: "100.2", messageTS: "100.2"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			locator := slackagent.NewThreadLocator("T123", test.channel, test.rootTS)
			envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{Type: "event_callback", Event: slackagent.Event{
				EventID: "Ev-" + test.name, EventType: test.eventType, UserID: "U456", Text: "hello", ChannelType: test.channelType,
				Conversation: slackagent.ConversationRef{TeamID: "T123", ConversationID: test.channel, ThreadID: test.rootTS},
				Message:      &slackagent.MessageRef{MessageID: test.messageTS},
			}}, testTimestamp())
			if err != nil || envelope.IgnoreEvent {
				t.Fatalf("BuildIngressEnvelope() = %+v, %v", envelope, err)
			}
			history := &threadHistoryStub{err: errors.New("must not be called")}
			processor := newInboundProcessor(inboundProcessorParams{
				SessionManager: existingSessionManager(t, locator, "slackagent:T123:U456"),
				Dispatcher:     &dispatcherRecorder{}, Lifecycle: &sessionLifecycleStub{}, History: history, Logger: zerolog.Nop(),
			})
			settlement, err := processor.ProcessInbound(context.Background(), envelope)
			if err != nil || settlement.Outcome != turncmd.InboundAccepted || history.calls != 0 {
				t.Fatalf("settlement/error/history = %+v/%v/%d", settlement, err, history.calls)
			}
		})
	}
}

func TestInboundProcessorDoesNotBeginLifecycleBeforeDispatchAcceptance(t *testing.T) {
	t.Parallel()
	locator := slackagent.NewThreadLocator("T123", "C456", "100.1")
	envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{Type: "event_callback", Event: slackagent.Event{
		EventID: "EvLifecycle", EventType: "app_mention", UserID: "U456", Text: "<@UBOT> help", ChannelType: "channel",
		Conversation: slackagent.ConversationRef{TeamID: "T123", ConversationID: "C456", ThreadID: "100.1"},
		Message:      &slackagent.MessageRef{MessageID: "100.1"},
	}}, testTimestamp())
	if err != nil {
		t.Fatalf("BuildIngressEnvelope() error = %v", err)
	}
	dispatcher := &dispatcherRecorder{err: actorlayer.TransientError(errors.New("queue unavailable"))}
	lifecycle := &sessionLifecycleStub{}
	processor := newInboundProcessor(inboundProcessorParams{
		SessionManager: existingSessionManager(t, locator, "slackagent:T123:U456"),
		Dispatcher:     dispatcher, Lifecycle: lifecycle, Logger: zerolog.Nop(),
	})
	settlement, err := processor.ProcessInbound(context.Background(), envelope)
	if err == nil || settlement.Outcome != turncmd.InboundRetry || len(lifecycle.begins) != 0 {
		t.Fatalf("settlement/error/begins = %+v/%v/%d", settlement, err, len(lifecycle.begins))
	}

	dispatcher.err = nil
	lifecycle.err = errors.New("Slack lifecycle unavailable")
	settlement, err = processor.ProcessInbound(context.Background(), envelope)
	if err == nil || settlement.Outcome != turncmd.InboundRetry || len(lifecycle.begins) != 1 {
		t.Fatalf("settlement/error/begins = %+v/%v/%d", settlement, err, len(lifecycle.begins))
	}
	if got := dispatcher.commands[len(dispatcher.commands)-1].DedupeKey; got != "slackagent:EvLifecycle" {
		t.Fatalf("dedupe key = %q", got)
	}
}

func TestInboundProcessorIgnoresUnknownAmbientChannelThread(t *testing.T) {
	t.Parallel()
	dispatcher := &dispatcherRecorder{}
	lifecycle := &sessionLifecycleStub{}
	envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{
		Type: "event_callback",
		Event: slackagent.Event{
			EventID:     "EvChannelReply",
			EventType:   "message",
			UserID:      "U456",
			Text:        "ambient reply",
			ChannelType: "channel",
			Conversation: slackagent.ConversationRef{
				TeamID:         "T123",
				ConversationID: "C456",
				ThreadID:       "1782234671.392669",
			},
			Message: &slackagent.MessageRef{MessageID: "1782234987.693923", ThreadTS: "1782234671.392669"},
		},
	}, testTimestamp())
	if err != nil {
		t.Fatalf("BuildIngressEnvelope() error = %v", err)
	}
	if !envelope.IgnoreEvent {
		t.Fatal("ambient channel reply must be ignored by Slackagent classification")
	}
	if len(dispatcher.commands) != 0 || len(lifecycle.begins) != 0 {
		t.Fatalf("commands/begins = %d/%d, want 0/0", len(dispatcher.commands), len(lifecycle.begins))
	}
}

type threadHistoryStub struct {
	snapshot slackagent.ThreadSnapshot
	err      error
	calls    int
}

func (s *threadHistoryStub) ReadThreadBefore(context.Context, string, string, string) (slackagent.ThreadSnapshot, error) {
	s.calls++
	return s.snapshot, s.err
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
	commands   []actorlayer.Envelope
	err        error
	receiptNil bool
	onDispatch func()
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
	if d.onDispatch != nil {
		d.onDispatch()
	}
	if d.err != nil {
		return nil, d.err
	}
	if d.receiptNil {
		return nil, nil
	}
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
	onBegin func()
}

func (s *sessionLifecycleStub) BeginTurn(_ context.Context, locator baldasession.SessionLocator, initiator, prompt string) error {
	s.begins = append(s.begins, beginCall{locator: locator, initiator: initiator, prompt: prompt})
	if s.onBegin != nil {
		s.onBegin()
	}
	return s.err
}

func contextualMentionEnvelope(t *testing.T, rootTS, messageTS string) slackagent.IngressEnvelope {
	t.Helper()
	envelope, err := slackagent.BuildIngressEnvelope(slackagent.EventEnvelope{Type: "event_callback", Event: slackagent.Event{
		EventID: "EvContext", EventType: "app_mention", UserID: "U456", Text: "<@UBOT> help", ChannelType: "channel",
		Conversation: slackagent.ConversationRef{TeamID: "T123", ConversationID: "C456", ThreadID: rootTS},
		Message:      &slackagent.MessageRef{MessageID: messageTS, ThreadTS: rootTS},
	}}, testTimestamp())
	if err != nil {
		t.Fatalf("BuildIngressEnvelope() error = %v", err)
	}
	return envelope
}

func existingSessionManager(t *testing.T, locator baldasession.SessionLocator, userID string) *baldasession.Manager {
	t.Helper()
	topic := &baldasession.TopicSession{}
	setPrivateField(t, topic, "sessionID", locator.SessionID)
	setPrivateField(t, topic, "userID", userID)
	manager := &baldasession.Manager{}
	setPrivateField(t, manager, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: topic})
	setPrivateField(t, manager, "sessionStore", restoreStoreStub{})
	return manager
}

func (s *sessionLifecycleStub) HandleSessionStopped(_ context.Context, locator baldasession.SessionLocator) error {
	s.stopped = append(s.stopped, locator)
	return s.err
}

func (s *sessionLifecycleStub) CloseSession(_ context.Context, locator baldasession.SessionLocator) error {
	s.closed = append(s.closed, locator)
	return s.err
}
