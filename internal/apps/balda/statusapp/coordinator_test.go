package statusapp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
)

type turnReaderStub struct {
	snapshot turnstatuscmd.Snapshot
	found    bool
	err      error
}

func (s turnReaderStub) Snapshot(context.Context, string) (turnstatuscmd.Snapshot, bool, error) {
	return s.snapshot, s.found, s.err
}

type questionReaderStub struct {
	record state.QuestionRecord
	found  bool
	err    error
}

func (s questionReaderStub) LatestPendingConversationQuestion(context.Context, string) (state.QuestionRecord, bool, error) {
	return s.record, s.found, s.err
}

type runtimeReaderStub struct {
	values map[string]any
	err    error
}

func (s runtimeReaderStub) ActiveRuntimeStateValue(_ context.Context, _ deliverycmd.Locator, key string) (any, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	value, ok := s.values[key]
	return value, ok, nil
}

func TestCoordinatorPrecedenceAndIsolation(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	active := turnstatuscmd.Snapshot{State: turnstatuscmd.StateActive, StartedAt: now.Add(-time.Minute)}
	locator := deliverycmd.Locator{SessionID: "session"}
	tests := []struct {
		name      string
		turns     turnReaderStub
		questions questionReaderStub
		runtime   runtimeReaderStub
		want      ConversationState
		waitKind  WaitingKind
		degraded  int
	}{
		{name: "question beats active turn", turns: turnReaderStub{snapshot: active, found: true}, questions: questionReaderStub{record: state.QuestionRecord{QuestionID: "q", ResumeTargetKind: "session", CreatedAt: now.Add(-time.Minute)}, found: true}, runtime: runtimeReaderStub{}, want: StateWaiting, waitKind: WaitingQuestion},
		{name: "permission wait", questions: questionReaderStub{record: state.QuestionRecord{QuestionID: "p", ResumeTargetKind: "permission"}, found: true}, runtime: runtimeReaderStub{}, want: StateWaiting, waitKind: WaitingPermission},
		{name: "loaded auto wait", runtime: runtimeReaderStub{values: map[string]any{automode.StateKeyEnabled: true, automode.StateKeyMode: automode.StateWaitingForUser, automode.StateKeyLastTurnAt: now.Format(time.RFC3339)}}, want: StateWaiting, waitKind: WaitingAuto},
		{name: "working with question read failure", turns: turnReaderStub{snapshot: active, found: true}, questions: questionReaderStub{err: errors.New("questions unavailable")}, runtime: runtimeReaderStub{}, want: StateWorking, degraded: 1},
		{name: "all unavailable", turns: turnReaderStub{err: errors.New("turn")}, questions: questionReaderStub{err: errors.New("question")}, runtime: runtimeReaderStub{err: errors.New("auto")}, want: StateUnavailable, degraded: 3},
		{name: "no active evidence", turns: turnReaderStub{}, questions: questionReaderStub{}, runtime: runtimeReaderStub{}, want: StateInactive},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := NewCoordinatorWithSources(test.turns, test.questions, test.runtime).Snapshot(t.Context(), locator)
			if got.State != test.want || len(got.Degraded) != test.degraded {
				t.Fatalf("Snapshot() = %+v", got)
			}
			if test.waitKind != "" && (!got.HasWait || got.Waiting.Kind != test.waitKind) {
				t.Fatalf("waiting = %+v", got.Waiting)
			}
		})
	}
}

func TestSnapshotElapsedSinceClampsNegativeValues(t *testing.T) {
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	if got := (Snapshot{}).ElapsedSince(now.Add(time.Second), now); got != 0 {
		t.Fatalf("ElapsedSince() = %s, want zero", got)
	}
}
