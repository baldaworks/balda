package jobs

import (
	"context"
	"fmt"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

type AgentStepsService struct {
	store ServiceStore
}

type agentStepsServiceParams struct {
	fx.In

	Store ServiceStore
}

func NewAgentStepsService(params agentStepsServiceParams) (*AgentStepsService, error) {
	if params.Store == nil {
		return nil, fmt.Errorf("agent step store is required")
	}
	return &AgentStepsService{store: params.Store}, nil
}

func (s *AgentStepsService) ReserveAgentStep(ctx context.Context, record baldastate.AgentStepRecord) (baldastate.AgentStepRecord, bool, error) {
	if s == nil {
		return baldastate.AgentStepRecord{}, false, nil
	}
	return s.store.ReserveAgentStep(ctx, record)
}

func (s *AgentStepsService) CompleteAgentStep(ctx context.Context, stepKey string, resultJSON string) error {
	if s == nil {
		return nil
	}
	return s.store.CompleteAgentStep(ctx, stepKey, resultJSON)
}

func (s *AgentStepsService) FailAgentStep(ctx context.Context, stepKey string, resultJSON string, reason string) error {
	if s == nil {
		return nil
	}
	return s.store.FailAgentStep(ctx, stepKey, resultJSON, reason)
}
