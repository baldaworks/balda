package actors

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	"github.com/rs/zerolog"
)

func TestPreparationFailureDeliveryPolicy(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name    string
		attempt int
		deliver bool
		cause   error
		want    int
	}{
		{"retry remains", 3, true, &turncmd.PreparationError{Cause: errors.New("secret")}, 0},
		{"exhausted", 4, true, &turncmd.PreparationError{Cause: errors.New("secret")}, 1},
		{"permanent", 0, true, &turncmd.PreparationError{Cause: actorlayer.PermanentError(errors.New("secret"))}, 1},
		{"provider owns failure", 4, true, errors.New("secret"), 0},
		{"canceled", 4, true, &turncmd.PreparationError{Cause: context.Canceled}, 0},
		{"silent invocation", 4, false, &turncmd.PreparationError{Cause: errors.New("secret")}, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			bus := &recordingHandlerCommandBus{}
			actor := NewSessionActor(SessionActorConfig{Dispatcher: bus})
			env := testSessionTurnEnvelope(t, nil)
			env.Attempt, env.MaxAttempts = test.attempt, 5
			var payload SessionTurnPayload
			if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			payload.Deliver = test.deliver
			if err := actor.reportPreparationFailure(context.Background(), env, payload, test.cause); err != nil {
				t.Fatal(err)
			}
			if len(bus.commands) != test.want {
				t.Fatalf("delivery count = %d, want %d", len(bus.commands), test.want)
			}
			if test.want == 0 {
				return
			}
			var delivery deliverycmd.Payload
			if err := actorlayer.UnmarshalPayload(bus.commands[0].Payload, &delivery); err != nil {
				t.Fatal(err)
			}
			if delivery.Text != preparationFailureMessage || strings.Contains(delivery.Text, "secret") {
				t.Fatalf("unsafe failure message: %q", delivery.Text)
			}
			if delivery.Settlement != deliverycmd.SettlementOutbox || delivery.Locator != payload.Locator {
				t.Fatalf("wrong delivery contract: %+v", delivery)
			}
		})
	}
}

func TestPreparationFailurePublicationFailureIsNotSuccess(t *testing.T) {
	bus := &recordingHandlerCommandBus{commandErrs: []error{errors.New("queue unavailable")}}
	actor := NewSessionActor(SessionActorConfig{Dispatcher: bus})
	env := testSessionTurnEnvelope(t, nil)
	env.Attempt, env.MaxAttempts = 4, 5
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Deliver = true
	err := actor.reportPreparationFailure(context.Background(), env, payload, &turncmd.PreparationError{Cause: errors.New("secret")})
	if err == nil || len(bus.commands) != 0 {
		t.Fatalf("publication failure = %v, commands = %d", err, len(bus.commands))
	}
}

func TestSessionActorReportsExhaustedPreparationFailure(t *testing.T) {
	ctx := context.Background()
	turns := NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() { _ = turns.Shutdown(context.Background()) })
	bus := &recordingHandlerCommandBus{}
	cause := &turncmd.PreparationError{Cause: errors.New("fixture error")}
	actor := NewSessionActor(SessionActorConfig{Turns: turns, Dispatcher: bus,
		Runner: callbackSessionTurnRunner{runFn: func(context.Context, SessionTurnPayload) error { return cause }}})
	env := testSessionTurnEnvelope(t, nil)
	env.Attempt, env.MaxAttempts = 4, 5
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Deliver = true
	data, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.Payload = data
	if err := actor.Handle(ctx, env); !errors.Is(err, cause) {
		t.Fatalf("Handle() = %v, want original failure for deadletter settlement", err)
	}
	if len(bus.commands) != 1 {
		t.Fatalf("failure deliveries = %d, want 1", len(bus.commands))
	}
}

func TestPreparationFailureDeliverySurvivesRestartWithoutDuplicate(t *testing.T) {
	ctx := context.Background()
	bus := &recordingHandlerCommandBus{}
	env := testSessionTurnEnvelope(t, nil)
	env.Attempt, env.MaxAttempts = 4, 5
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Deliver = true
	reportTo := deliverycmd.Locator{SessionID: "report-session", ChannelType: "mattermost", AddressKey: "channel:thread", AddressJSON: `{"channel_id":"channel","root_id":"thread"}`}
	payload.ReportTo = &reportTo
	for range 2 {
		actor := NewSessionActor(SessionActorConfig{Dispatcher: bus})
		if err := actor.reportPreparationFailure(ctx, env, payload, &turncmd.PreparationError{Cause: errors.New("private diagnostic")}); err != nil {
			t.Fatal(err)
		}
	}
	if len(bus.commands) != 2 || bus.commands[0].ID != bus.commands[1].ID || bus.commands[0].Payload.String() != bus.commands[1].Payload.String() {
		t.Fatal("redelivery changed the durable delivery identity or payload")
	}
	var delivery deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(bus.commands[0].Payload, &delivery); err != nil {
		t.Fatal(err)
	}
	if delivery.Locator != reportTo {
		t.Fatal("failure did not use the exact report target")
	}
	path := filepath.Join(t.TempDir(), "delivery.db")
	store, err := baldastate.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	transport := &preparationDeliveryTransport{err: deliverycmd.RetryableError(errors.New("transport unavailable"))}
	service := deliveryworkflow.New(transport, store.Jobs(), nil, nil, nil, zerolog.Nop())
	if err := service.Handle(ctx, bus.commands[0], delivery); err == nil {
		t.Fatal("unavailable transport returned success")
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err = baldastate.NewSQLiteProvider(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	transport.err = nil
	service = deliveryworkflow.New(transport, store.Jobs(), nil, nil, nil, zerolog.Nop())
	for _, command := range bus.commands {
		if err := service.Handle(ctx, command, delivery); err != nil {
			t.Fatal(err)
		}
	}
	if transport.sent != 1 {
		t.Fatalf("sent = %d, want 1 after restart and duplicate", transport.sent)
	}
}

type preparationDeliveryTransport struct {
	err  error
	sent int
}

func (d *preparationDeliveryTransport) Dispatch(context.Context, deliveryworkflow.Delivery) (string, error) {
	if d.err != nil {
		return "", d.err
	}
	d.sent++
	return "fixture-message", nil
}
