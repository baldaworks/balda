package statusapp

import (
	"context"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/automode"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatusapp"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
)

type ConversationState string

const (
	StateWaiting     ConversationState = "waiting"
	StateWorking     ConversationState = "working"
	StateInactive    ConversationState = "inactive"
	StateUnavailable ConversationState = "unavailable"
)

type WaitingKind string

const (
	WaitingQuestion   WaitingKind = "question"
	WaitingPermission WaitingKind = "permission"
	WaitingAuto       WaitingKind = "auto"
)

type Waiting struct {
	Kind       WaitingKind
	Since      time.Time
	QuestionID string
}

type Snapshot struct {
	State    ConversationState
	Turn     turnstatuscmd.Snapshot
	HasTurn  bool
	Waiting  Waiting
	HasWait  bool
	Degraded []string
}

func (s Snapshot) ElapsedSince(at, now time.Time) time.Duration {
	if at.IsZero() || now.Before(at) {
		return 0
	}
	return now.Sub(at)
}

type turnReader interface {
	Snapshot(ctx context.Context, sessionID string) (turnstatuscmd.Snapshot, bool, error)
}

type questionReader interface {
	LatestPendingConversationQuestion(ctx context.Context, sessionID string) (state.QuestionRecord, bool, error)
}

type activeRuntimeReader interface {
	ActiveRuntimeStateValue(ctx context.Context, locator deliverycmd.Locator, key string) (any, bool, error)
}

type Coordinator struct {
	turns     turnReader
	questions questionReader
	runtime   activeRuntimeReader
}

func NewCoordinator(provider state.Provider, turns *turnstatusapp.Recorder, runtime *session.Manager) *Coordinator {
	return &Coordinator{turns: turns, questions: provider.Questions(), runtime: runtime}
}

func NewCoordinatorWithSources(turns turnReader, questions questionReader, runtime activeRuntimeReader) *Coordinator {
	return &Coordinator{turns: turns, questions: questions, runtime: runtime}
}

func (c *Coordinator) Snapshot(ctx context.Context, locator deliverycmd.Locator) Snapshot {
	result := Snapshot{State: StateInactive}
	sessionID := strings.TrimSpace(locator.SessionID)
	failedSources := 0
	availableSources := 0
	if c.turns != nil {
		turn, found, err := c.turns.Snapshot(ctx, sessionID)
		if err != nil {
			failedSources++
			result.Degraded = append(result.Degraded, "turn")
		} else {
			availableSources++
			result.Turn, result.HasTurn = turn, found
		}
	}
	if c.questions != nil {
		question, found, err := c.questions.LatestPendingConversationQuestion(ctx, sessionID)
		if err != nil {
			failedSources++
			result.Degraded = append(result.Degraded, "question")
		} else {
			availableSources++
			if found {
				kind := WaitingQuestion
				if question.ResumeTargetKind == "permission" {
					kind = WaitingPermission
				}
				result.Waiting = Waiting{Kind: kind, Since: question.CreatedAt, QuestionID: question.QuestionID}
				result.HasWait = true
			}
		}
	}
	if c.runtime != nil {
		waiting, since, err := c.autoWaiting(ctx, locator)
		if err != nil {
			failedSources++
			result.Degraded = append(result.Degraded, "auto")
		} else {
			availableSources++
			if waiting && !result.HasWait {
				result.Waiting = Waiting{Kind: WaitingAuto, Since: since}
				result.HasWait = true
			}
		}
	}
	switch {
	case result.HasWait:
		result.State = StateWaiting
	case result.HasTurn && result.Turn.State == turnstatuscmd.StateActive:
		result.State = StateWorking
	case availableSources == 0 && failedSources > 0:
		result.State = StateUnavailable
	default:
		result.State = StateInactive
	}
	return result
}

func (c *Coordinator) autoWaiting(ctx context.Context, locator deliverycmd.Locator) (bool, time.Time, error) {
	enabledValue, enabledFound, err := c.runtime.ActiveRuntimeStateValue(ctx, locator, automode.StateKeyEnabled)
	if err != nil || !enabledFound || !automode.ParseBool(enabledValue) {
		return false, time.Time{}, err
	}
	stateValue, stateFound, err := c.runtime.ActiveRuntimeStateValue(ctx, locator, automode.StateKeyMode)
	if err != nil || !stateFound || strings.TrimSpace(asString(stateValue)) != automode.StateWaitingForUser {
		return false, time.Time{}, err
	}
	lastTurnValue, _, err := c.runtime.ActiveRuntimeStateValue(ctx, locator, automode.StateKeyLastTurnAt)
	if err != nil {
		return false, time.Time{}, err
	}
	since, _ := time.Parse(time.RFC3339, strings.TrimSpace(asString(lastTurnValue)))
	return true, since, nil
}

func asString(value any) string {
	text, _ := value.(string)
	return text
}
