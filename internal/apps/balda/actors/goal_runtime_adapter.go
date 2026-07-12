package actors

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/actors/goalkeeper"
	baldaagent "github.com/normahq/balda/internal/apps/balda/agent"
)

type goalRunAdapter struct {
	runtime *baldaagent.GoalRun
}

func (a goalRunAdapter) Runner() goalkeeper.GoalRunner {
	return a.runtime.Runner
}

func (a goalRunAdapter) SessionID() string {
	return a.runtime.SessionID
}

func (a goalRunAdapter) WorkspaceDir() string {
	return a.runtime.WorkspaceDir
}

func (a goalRunAdapter) BranchName() string {
	return a.runtime.BranchName
}

func (a goalRunAdapter) Close() error {
	return a.runtime.Close()
}

func (a goalRunAdapter) CleanupResources(ctx context.Context) error {
	return a.runtime.CleanupResources(ctx)
}

func (a goalRunAdapter) Finalize(
	ctx context.Context,
	objective string,
	workerOutput string,
	validatorOutput string,
) (goalkeeper.GoalFinalizationResult, error) {
	result, err := a.runtime.Finalize(ctx, objective, workerOutput, validatorOutput)
	return goalkeeper.GoalFinalizationResult{
		Status:        result.Status,
		CommitMessage: result.CommitMessage,
		Reason:        result.Reason,
		Error:         result.Error,
	}, err
}
