package actors

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
	"github.com/rs/zerolog"
)

type statusRecorderSpy struct {
	mu       sync.Mutex
	begins   int
	outcomes []turnstatuscmd.Outcome
}

func (s *statusRecorderSpy) Begin(ctx context.Context, sessionID, archiveID string, _ time.Time) (context.Context, error) {
	s.mu.Lock()
	s.begins++
	s.mu.Unlock()
	return turnstatuscmd.ContextWithIdentity(ctx, turnstatuscmd.Identity{SessionID: sessionID, ArchiveID: archiveID, TurnID: "turn"}), nil
}

func (*statusRecorderSpy) Observe(context.Context, turnstatuscmd.ActivityKind, time.Time) error {
	return nil
}

func (s *statusRecorderSpy) Complete(ctx context.Context, outcome turnstatuscmd.Outcome, _ time.Time) error {
	if _, ok := turnstatuscmd.IdentityFromContext(ctx); !ok {
		return nil
	}
	s.mu.Lock()
	s.outcomes = append(s.outcomes, outcome)
	s.mu.Unlock()
	return nil
}

func TestTurnDispatcherRecordsConversationLifecycle(t *testing.T) {
	recorder := &statusRecorderSpy{}
	dispatcher := NewTurnDispatcherWithStatus(zerolog.Nop(), recorder)
	result, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
		SessionID:   "session",
		SessionTurn: &turncmd.SessionTurnPayload{ArchiveID: "archive", Source: turncmd.SourceTelegram},
		Run: func(ctx context.Context) error {
			if _, ok := turnstatuscmd.IdentityFromContext(ctx); !ok {
				t.Fatal("turn identity missing from run context")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.begins != 1 || len(recorder.outcomes) != 1 || recorder.outcomes[0] != turnstatuscmd.OutcomeSucceeded {
		t.Fatalf("recorded begins=%d outcomes=%v", recorder.begins, recorder.outcomes)
	}
}

func TestTurnDispatcherExcludesPrivateRunsFromConversationStatus(t *testing.T) {
	for _, source := range []string{turncmd.SourceSchedule, turncmd.SourceWebhook} {
		t.Run(source, func(t *testing.T) {
			recorder := &statusRecorderSpy{}
			dispatcher := NewTurnDispatcherWithStatus(zerolog.Nop(), recorder)
			result, _, err := dispatcher.Enqueue(t.Context(), TurnTask{
				SessionID: "private", SessionTurn: &turncmd.SessionTurnPayload{ArchiveID: "archive", Source: source},
				Run: func(context.Context) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := <-result; err != nil {
				t.Fatal(err)
			}
			if recorder.begins != 0 {
				t.Fatalf("private run recorded %d starts", recorder.begins)
			}
		})
	}
}
