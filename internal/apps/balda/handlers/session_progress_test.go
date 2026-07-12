package handlers

import (
	"context"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
)

type captureDispatcher struct {
	envs []actorlayer.Envelope
}

func (d *captureDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envs = append(d.envs, env)
	return &actortransport.DispatchReceipt{}, nil
}

func TestSessionProgressDispatcherPlanThinkingActivityFlow(t *testing.T) {
	dispatcher := &captureDispatcher{}
	emitter := newSessionProgressDispatcher(
		dispatcher,
		actorlayer.ActorAddress{Target: "session", Key: "s1"},
		baldasession.SessionLocator{ChannelType: "telegram", SessionID: "sess", AddressKey: "chat/topic"},
		"job-1",
		42,
		deliveryfmt.ProgressPolicy{Thinking: true, PlanUpdates: true},
		false,
		zerolog.Nop(),
	)

	result, err := emitter.HandleNonTerminal(context.Background(), sessionProgressUpdate{
		Plan:             progress.PlanSnapshot{Entries: []progress.PlanEntry{{Content: "Inspect", Status: "completed"}}},
		PlanProgressText: "[completed] Inspect",
		HasPlanUpdate:    true,
	})
	if err != nil {
		t.Fatalf("plan progress: %v", err)
	}
	if result.DispatchedPlanText != "[completed] Inspect" {
		t.Fatalf("unexpected plan text %q", result.DispatchedPlanText)
	}
	assertProgressKind(t, dispatcher.envs, 0, deliverycmd.ProgressPlanUpdate, true, 0)

	result, err = emitter.HandleNonTerminal(context.Background(), sessionProgressUpdate{
		ReasoningText:    "hidden reasoning",
		HasThoughtUpdate: true,
	})
	if err != nil {
		t.Fatalf("thinking after plan: %v", err)
	}
	if !result.SentProgress {
		t.Fatalf("expected hidden thinking progress to count as sent")
	}
	assertProgressKind(t, dispatcher.envs, 1, deliverycmd.ProgressThinking, true, 2)

	result, err = emitter.HandleNonTerminal(context.Background(), sessionProgressUpdate{})
	if err != nil {
		t.Fatalf("activity fallback: %v", err)
	}
	if result.SentProgress {
		t.Fatalf("activity fallback should not report sent draft progress")
	}
	assertProgressKind(t, dispatcher.envs, 2, deliverycmd.ProgressActivity, false, 3)
}

func TestSessionProgressDispatcherVisibleThinkingTracksDeliverySequence(t *testing.T) {
	dispatcher := &captureDispatcher{}
	emitter := newSessionProgressDispatcher(
		dispatcher,
		actorlayer.ActorAddress{Target: "session", Key: "s1"},
		baldasession.SessionLocator{ChannelType: "telegram", SessionID: "sess", AddressKey: "chat/topic"},
		"",
		42,
		deliveryfmt.ProgressPolicy{Thinking: true},
		false,
		zerolog.Nop(),
	)

	_, err := emitter.HandleNonTerminal(context.Background(), sessionProgressUpdate{
		ReasoningText:    "first",
		HasThoughtUpdate: true,
	})
	if err != nil {
		t.Fatalf("first visible thinking: %v", err)
	}
	_, err = emitter.HandleNonTerminal(context.Background(), sessionProgressUpdate{
		ReasoningText:    "second",
		HasThoughtUpdate: true,
	})
	if err != nil {
		t.Fatalf("second visible thinking: %v", err)
	}
	assertProgressKind(t, dispatcher.envs, 0, deliverycmd.ProgressThinking, true, 1)
	assertProgressKind(t, dispatcher.envs, 1, deliverycmd.ProgressThinking, true, 2)
}

func assertProgressKind(t *testing.T, envs []actorlayer.Envelope, idx int, wantKind deliverycmd.ProgressKind, wantVisible bool, wantSequence int) {
	t.Helper()
	if idx >= len(envs) {
		t.Fatalf("missing envelope %d, have %d", idx, len(envs))
	}
	var payload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(envs[idx].Payload, &payload); err != nil {
		t.Fatalf("decode payload %d: %v", idx, err)
	}
	if payload.Progress == nil {
		t.Fatalf("payload %d missing progress", idx)
	}
	if payload.Progress.Kind != wantKind {
		t.Fatalf("payload %d progress kind = %q, want %q", idx, payload.Progress.Kind, wantKind)
	}
	if payload.Progress.Visible != wantVisible {
		t.Fatalf("payload %d visible = %v, want %v", idx, payload.Progress.Visible, wantVisible)
	}
	if payload.Progress.Sequence != wantSequence {
		t.Fatalf("payload %d sequence = %d, want %d", idx, payload.Progress.Sequence, wantSequence)
	}
}
