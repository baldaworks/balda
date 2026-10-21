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
	})
	assertCurrentTurn(t, dispatcher, "session-b", appports.CurrentTurn{
		SessionID:       "session-b",
		RequesterUserID: "requester-b",
	})
	assertCurrentTurn(t, dispatcher, "session-service", appports.CurrentTurn{SessionID: "session-service"})

	identity, ok := dispatcher.CurrentTurn("session-a")
	if !ok {
		t.Fatal("CurrentTurn(session-a) = absent, want present")
	}
	identity.RequesterUserID = "changed"
	assertCurrentTurn(t, dispatcher, "session-a", appports.CurrentTurn{
		SessionID:       "session-a",
		RequesterUserID: "requester-a",
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
	if got != want {
		t.Errorf("CurrentTurn(%q) = %+v, want %+v", sessionID, got, want)
	}
}
