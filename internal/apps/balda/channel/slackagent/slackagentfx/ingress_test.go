package slackagentfx

import (
	"context"
	"reflect"
	"testing"
	"time"
	"unsafe"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
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
	processor := newInboundProcessor(inboundProcessorParams{
		SessionManager: manager,
		Dispatcher:     dispatcher,
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

func testTimestamp() time.Time {
	return time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
}

type dispatcherRecorder struct {
	commands []actorlayer.Envelope
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
