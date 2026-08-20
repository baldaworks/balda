package sessionturnapp

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/automodecmd"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestAutoDecisionNotificationSuppressesSentinels(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name         string
		turnSource   string
		responseText string
		status       automode.Status
		wantText     string
		wantSource   string
		wantOK       bool
	}{
		{
			name:         "done",
			turnSource:   turncmd.SourceAuto,
			responseText: automode.DoneSentinel,
			status:       automode.Status{Enabled: true, State: automode.StateRunning, ConsecutiveTurns: 3, MaxTurns: automode.DefaultMaxTurns},
			wantText: automode.RenderCompactStatusMarkdown(automode.Status{
				Enabled:          true,
				State:            automode.StateIdle,
				ConsecutiveTurns: 3,
				MaxTurns:         automode.DefaultMaxTurns,
				LastTurnAt:       "2026-07-27T18:00:00Z",
				LastStopReason:   "model_reported_done",
			}),
			wantSource: "auto_done",
			wantOK:     true,
		},
		{
			name:         "wait",
			turnSource:   turncmd.SourceAuto,
			responseText: automode.WaitSentinel,
			status:       automode.Status{Enabled: true, State: automode.StateRunning, ConsecutiveTurns: 2, MaxTurns: automode.DefaultMaxTurns},
			wantText: automode.RenderCompactStatusMarkdown(automode.Status{
				Enabled:          true,
				State:            automode.StateWaitingForUser,
				ConsecutiveTurns: 2,
				MaxTurns:         automode.DefaultMaxTurns,
				LastTurnAt:       "2026-07-27T18:00:00Z",
				LastStopReason:   "model_waiting_for_user",
			}),
			wantSource: "auto_wait_for_user",
			wantOK:     true,
		},
		{name: "visible auto response", turnSource: turncmd.SourceAuto, responseText: "continue", status: automode.DefaultStatusWithMaxTurns(automode.DefaultMaxTurns), wantOK: false},
		{name: "ordinary turn", turnSource: turncmd.SourceTelegram, responseText: automode.DoneSentinel, status: automode.DefaultStatusWithMaxTurns(automode.DefaultMaxTurns), wantOK: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gotText, gotSource, gotOK := autoDecisionNotification(tt.status, tt.turnSource, tt.responseText, time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC))
			if gotText != tt.wantText || gotSource != tt.wantSource || gotOK != tt.wantOK {
				t.Fatalf("autoDecisionNotification() = %q, %q, %v; want %q, %q, %v", gotText, gotSource, gotOK, tt.wantText, tt.wantSource, tt.wantOK)
			}
		})
	}
}

type fakeAutoRuntimeState struct {
	state map[string]any
}

func (f *fakeAutoRuntimeState) RuntimeStateValue(_ context.Context, _ baldasession.SessionLocator, key string) (any, bool, error) {
	if f == nil || f.state == nil {
		return nil, false, nil
	}
	value, ok := f.state[key]
	return value, ok, nil
}

func (f *fakeAutoRuntimeState) UpdateRuntimeState(_ context.Context, _ baldasession.SessionLocator, state map[string]any) error {
	if f.state == nil {
		f.state = map[string]any{}
	}
	for key, value := range state {
		f.state[key] = value
	}
	return nil
}

type fakeAutoDispatcher struct {
	envelopes []actorlayer.Envelope
	state     *fakeAutoRuntimeState
}

func (f *fakeAutoDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if env.Namespace == baldaexecution.NamespaceAutoModeCommand && f.state != nil {
		var payload automodecmd.Payload
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			return nil, err
		}
		if err := f.state.UpdateRuntimeState(context.Background(), payload.Locator, payload.State); err != nil {
			return nil, err
		}
	}
	f.envelopes = append(f.envelopes, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestMaybeScheduleAutoTurnDispatchesSyntheticTurn(t *testing.T) {
	t.Parallel()

	state := &fakeAutoRuntimeState{
		state: map[string]any{
			automode.StateKeyEnabled:  true,
			automode.StateKeyMode:     automode.StateIdle,
			automode.StateKeyMaxTurns: automode.DefaultMaxTurns,
		},
	}
	dispatcher := &fakeAutoDispatcher{state: state}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, state, zerolog.Nop(), automode.DefaultMaxTurns)
	locator := baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"}

	err := service.maybeScheduleAutoTurn(context.Background(), ExecutionRequest{
		UserID:          "tg-101",
		RequesterUserID: "tg-101",
		AgentSessionID:  "tg-1-0",
		Locator:         locator,
		DedupeKey:       "human-turn-1",
		DeliveryOptions: turncmd.NormalizeSessionDeliveryOptions(turncmd.SessionTurnPayload{}),
	}, "streamed_text", "visible output")
	if err != nil {
		t.Fatalf("maybeScheduleAutoTurn() error = %v", err)
	}
	if len(dispatcher.envelopes) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(dispatcher.envelopes))
	}
	var payload turncmd.SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(dispatcher.envelopes[1].Payload, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if payload.Source != turncmd.SourceAuto {
		t.Fatalf("payload.Source = %q, want auto", payload.Source)
	}
	if payload.Text != automode.InternalPrompt(automode.DefaultMaxTurns) {
		t.Fatalf("payload.Text = %q, want internal prompt", payload.Text)
	}
	if got, want := dispatcher.envelopes[1].DedupeKey, autoTurnDedupeKey(locator.SessionID, "human-turn-1", 1); got != want {
		t.Fatalf("dedupe key = %q, want %q", got, want)
	}
	if payload.DedupeKey != dispatcher.envelopes[1].DedupeKey {
		t.Fatalf("payload dedupe key = %q, want envelope dedupe key %q", payload.DedupeKey, dispatcher.envelopes[1].DedupeKey)
	}
	if got := automode.ParseInt(state.state[automode.StateKeyConsecutiveTurns], 0); got != 1 {
		t.Fatalf("consecutive turns state = %d, want 1", got)
	}
}

func TestStartAutoCycleIfNeededTransitionsToRunningImmediately(t *testing.T) {
	t.Parallel()

	state := &fakeAutoRuntimeState{
		state: map[string]any{
			automode.StateKeyEnabled:  true,
			automode.StateKeyMode:     automode.StateIdle,
			automode.StateKeyMaxTurns: automode.DefaultMaxTurns,
		},
	}
	dispatcher := &fakeAutoDispatcher{state: state}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, state, zerolog.Nop(), automode.DefaultMaxTurns)
	service.now = func() time.Time { return time.Date(2026, 7, 27, 18, 20, 0, 0, time.UTC) }
	locator := baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"}

	if err := service.startAutoCycleIfNeeded(context.Background(), ExecutionRequest{
		UserID:          "tg-101",
		RequesterUserID: "tg-101",
		Locator:         locator,
		TurnSource:      turncmd.SourceTelegram,
		DeliveryOptions: turncmd.NormalizeSessionDeliveryOptions(turncmd.SessionTurnPayload{}),
	}); err != nil {
		t.Fatalf("startAutoCycleIfNeeded() error = %v", err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatches = %d, want only state update without visible running notification", len(dispatcher.envelopes))
	}
	if got := state.state[automode.StateKeyMode]; got != automode.StateRunning {
		t.Fatalf("state mode = %#v, want %q", got, automode.StateRunning)
	}
}

func TestNotifyAutoStateChangeSuppressesRunningNotification(t *testing.T) {
	t.Parallel()

	dispatcher := &fakeAutoDispatcher{}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)

	err := service.notifyAutoStateChange(context.Background(), ExecutionRequest{
		Locator:         baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"},
		DeliveryOptions: turncmd.NormalizeSessionDeliveryOptions(turncmd.SessionTurnPayload{}),
	}, automode.Status{
		Enabled: true,
		State:   automode.StateRunning,
	})
	if err != nil {
		t.Fatalf("notifyAutoStateChange() error = %v", err)
	}
	if len(dispatcher.envelopes) != 0 {
		t.Fatalf("dispatches = %d, want 0 for suppressed running state", len(dispatcher.envelopes))
	}
}

func TestNotifyAutoStateChangeDeliversWaitingForUserNotification(t *testing.T) {
	t.Parallel()

	dispatcher := &fakeAutoDispatcher{}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)

	err := service.notifyAutoStateChange(context.Background(), ExecutionRequest{
		Locator:         baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"},
		DeliveryOptions: turncmd.NormalizeSessionDeliveryOptions(turncmd.SessionTurnPayload{}),
	}, automode.Status{
		Enabled: true,
		State:   automode.StateWaitingForUser,
	})
	if err != nil {
		t.Fatalf("notifyAutoStateChange() error = %v", err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(dispatcher.envelopes))
	}
}

func TestNotifyAutoStateChangeDeliversIdleNotification(t *testing.T) {
	t.Parallel()

	dispatcher := &fakeAutoDispatcher{}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, nil, zerolog.Nop(), automode.DefaultMaxTurns)

	err := service.notifyAutoStateChange(context.Background(), ExecutionRequest{
		Locator:         baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"},
		DeliveryOptions: turncmd.NormalizeSessionDeliveryOptions(turncmd.SessionTurnPayload{}),
	}, automode.Status{
		Enabled: true,
		State:   automode.StateIdle,
	})
	if err != nil {
		t.Fatalf("notifyAutoStateChange() error = %v", err)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatches = %d, want 1", len(dispatcher.envelopes))
	}
}

func TestAutoTurnDedupeKeySeparatesContinuationChains(t *testing.T) {
	t.Parallel()

	first := autoTurnDedupeKey("tg-1-0", "human-turn-1", 1)
	retry := autoTurnDedupeKey("tg-1-0", "human-turn-1", 1)
	second := autoTurnDedupeKey("tg-1-0", "human-turn-2", 1)
	if first != retry {
		t.Fatalf("retry dedupe key = %q, want %q", retry, first)
	}
	if first == second {
		t.Fatalf("independent continuation chains share dedupe key %q", first)
	}
}

func TestMaybeScheduleAutoTurnStopsOnNoProgressForAutoTurns(t *testing.T) {
	t.Parallel()

	state := &fakeAutoRuntimeState{
		state: map[string]any{
			automode.StateKeyEnabled:          true,
			automode.StateKeyMode:             automode.StateRunning,
			automode.StateKeyMaxTurns:         automode.DefaultMaxTurns,
			automode.StateKeyConsecutiveTurns: 1,
			automode.StateKeyLastOutput:       "same-output",
		},
	}
	dispatcher := &fakeAutoDispatcher{state: state}
	service := NewTurnExecutionServiceWithJobEvents(dispatcher, nil, state, zerolog.Nop(), automode.DefaultMaxTurns)
	locator := baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0"}

	err := service.maybeScheduleAutoTurn(context.Background(), ExecutionRequest{
		UserID:          "tg-101",
		RequesterUserID: "tg-101",
		AgentSessionID:  "tg-1-0",
		Locator:         locator,
		TurnSource:      turncmd.SourceAuto,
	}, "same-output", "same-output")
	if err != nil {
		t.Fatalf("maybeScheduleAutoTurn() error = %v", err)
	}
	if len(dispatcher.envelopes) != 2 {
		t.Fatalf("dispatches = %d, want 2", len(dispatcher.envelopes))
	}
	if got := state.state[automode.StateKeyMode]; got != automode.StateNoProgress {
		t.Fatalf("state mode = %#v, want %q", got, automode.StateNoProgress)
	}
	if got := state.state[automode.StateKeyLastStopReason]; got != "repeated_visible_output" {
		t.Fatalf("last stop reason = %#v, want repeated_visible_output", got)
	}
}
