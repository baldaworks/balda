package actors

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestTurnDispatcherCurrentTurnLifecycleAndIsolation(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	if _, ok := dispatcher.CurrentTurn("session-a"); ok {
		t.Fatal("CurrentTurn before execution = present, want absent")
	}

	release := make(chan struct{})
	startedA := make(chan struct{})
	startedB := make(chan struct{})
	startedService := make(chan struct{})

	resultA := enqueueCurrentTurnTest(t, dispatcher, "session-a", " requester-a ", startedA, release)
	resultB := enqueueCurrentTurnTest(t, dispatcher, "session-b", "requester-b", startedB, release)
	resultService := enqueueCurrentTurnTest(t, dispatcher, "session-service", "", startedService, release)
	waitForSignal(t, startedA, "session A start")
	waitForSignal(t, startedB, "session B start")
	waitForSignal(t, startedService, "service session start")

	assertCurrentTurn(t, dispatcher, "session-a", appports.CurrentTurn{
		SessionID:       "session-a",
		RequesterUserID: "requester-a",
		Text:            "text-session-a",
	})
	assertCurrentTurn(t, dispatcher, "session-b", appports.CurrentTurn{
		SessionID:       "session-b",
		RequesterUserID: "requester-b",
		Text:            "text-session-b",
	})
	assertCurrentTurn(t, dispatcher, "session-service", appports.CurrentTurn{
		SessionID: "session-service",
		Text:      "text-session-service",
	})

	identity, ok := dispatcher.CurrentTurn("session-a")
	if !ok {
		t.Fatal("CurrentTurn(session-a) = absent, want present")
	}
	identity.RequesterUserID = "changed"
	assertCurrentTurn(t, dispatcher, "session-a", appports.CurrentTurn{
		SessionID:       "session-a",
		RequesterUserID: "requester-a",
		Text:            "text-session-a",
	})

	close(release)
	if err := <-resultA; err != nil {
		t.Fatalf("session A result = %v", err)
	}
	if err := <-resultB; err != nil {
		t.Fatalf("session B result = %v", err)
	}
	if err := <-resultService; err != nil {
		t.Fatalf("service session result = %v", err)
	}

	if _, ok := dispatcher.CurrentTurn("session-a"); ok {
		t.Fatal("CurrentTurn after execution = present, want absent")
	}
	if _, ok := dispatcher.CurrentTurn("session-b"); ok {
		t.Fatal("CurrentTurn after execution = present, want absent")
	}
	if _, ok := dispatcher.CurrentTurn("session-service"); ok {
		t.Fatal("CurrentTurn after execution = present, want absent")
	}
}

func TestTurnDispatcherCurrentTurnReportsPendingTurns(t *testing.T) {
	t.Parallel()

	dispatcher := NewTurnDispatcher(zerolog.Nop())
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	release := make(chan struct{})
	started := make(chan struct{})
	running := enqueueCurrentTurnTest(t, dispatcher, "session", "requester", started, release)
	waitForSignal(t, started, "running turn start")

	pending, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID: "session",
		SessionTurn: &turncmd.SessionTurnPayload{
			RequesterUserID: "requester",
			Text:            "pending text",
		},
		Run: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatalf("Enqueue(pending) error = %v", err)
	}

	got, ok := dispatcher.CurrentTurn("session")
	if !ok {
		t.Fatal("CurrentTurn(session) = absent, want present")
	}
	if got.PendingTurns != 1 {
		t.Errorf("CurrentTurn(session).PendingTurns = %d, want 1", got.PendingTurns)
	}
	if got.Text != "text-session" {
		t.Errorf("CurrentTurn(session).Text = %q, want running turn text", got.Text)
	}

	close(release)
	if err := <-running; err != nil {
		t.Fatalf("running result = %v", err)
	}
	if err := <-pending; err != nil {
		t.Fatalf("pending result = %v", err)
	}
}

func enqueueCurrentTurnTest(
	t *testing.T,
	dispatcher *TurnDispatcher,
	sessionID string,
	requesterUserID string,
	started chan<- struct{},
	release <-chan struct{},
) <-chan error {
	t.Helper()

	result, _, err := dispatcher.Enqueue(context.Background(), TurnTask{
		SessionID: sessionID,
		SessionTurn: &turncmd.SessionTurnPayload{
			RequesterUserID: requesterUserID,
			Text:            "text-" + sessionID,
		},
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Enqueue(%s) error = %v", sessionID, err)
	}

	return result
}

func assertCurrentTurn(t *testing.T, dispatcher *TurnDispatcher, sessionID string, want appports.CurrentTurn) {
	t.Helper()

	got, ok := dispatcher.CurrentTurn(sessionID)
	if !ok {
		t.Fatalf("CurrentTurn(%q) = absent, want %+v", sessionID, want)
	}
	if got.SessionID != want.SessionID || got.RequesterUserID != want.RequesterUserID || got.Text != want.Text || got.PendingTurns != want.PendingTurns {
		t.Errorf("CurrentTurn(%q) = %+v, want fields %+v", sessionID, got, want)
	}
	if got.StartedAt.IsZero() {
		t.Errorf("CurrentTurn(%q).StartedAt is zero", sessionID)
	}
}
