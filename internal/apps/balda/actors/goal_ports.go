package actors

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/actors/goalkeeper"
	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type goalkeeperJobLifecycle interface {
	Create(ctx context.Context, record baldastate.JobRecord, actor string, payload any) (bool, error)
	Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error)
	ListActiveGoalJobsBySession(ctx context.Context, sessionID string) ([]baldastate.JobRecord, error)
	MarkStatus(ctx context.Context, jobID string, status string, actor string, messageID string, reason string, payload any) error
	SetResult(ctx context.Context, jobID string, result any, status string, actor string, reason string) error
}

type goalkeeperJobEvents interface {
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

type goalRunPreparerPort interface {
	PrepareGoalRun(ctx context.Context, cfg goalkeeper.GoalRunConfig) (goalkeeper.GoalRun, error)
}

type runtimeGoalRunPreparer struct {
	manager *baldaagent.RuntimeManager
}

func (a runtimeGoalRunPreparer) PrepareGoalRun(ctx context.Context, cfg goalkeeper.GoalRunConfig) (goalkeeper.GoalRun, error) {
	runtime, err := a.manager.PrepareGoalRun(ctx, baldaagent.GoalRunConfig{
		SourceSessionID: cfg.SourceSessionID,
		JobID:           cfg.JobID,
		UserID:          cfg.UserID,
		MaxIterations:   cfg.MaxIterations,
	})
	if err != nil {
		return nil, err
	}
	return goalRunAdapter{runtime: runtime}, nil
}
