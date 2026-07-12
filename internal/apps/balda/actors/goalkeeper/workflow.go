package goalkeeper

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/progress"
	adkagent "google.golang.org/adk/v2/agent"
	"google.golang.org/genai"
)

// workflow.go owns the runtime event loop for worker/validator goal execution.
func (c *coordinator) runWorkflow(
	ctx context.Context,
	runtime GoalRun,
	userID string,
	agentSessionID string,
	payload goalJobPayload,
) (goalRunResult, error) {
	progressEmitter := newGoalProgressEmitter(c.jobs, c.events, c.dispatcher)
	result := goalRunResult{payload: payload}
	if runtime == nil || runtime.Runner() == nil {
		return result, fmt.Errorf("goal runner is required")
	}
	userContent := genai.NewContentFromText("Goal:\n"+strings.TrimSpace(payload.Objective), genai.RoleUser)
	currentStep := ""
	sawTurnComplete := false
	deliverySeq := 0
	stepStates := map[string]*stepProgressState{
		WorkerStep:    {},
		ValidatorStep: {},
	}
	for ev, err := range runtime.Runner().Run(ctx, userID, agentSessionID, userContent, adkagent.RunConfig{}) {
		if err != nil {
			return result, fmt.Errorf("run goal workflow: %w", err)
		}
		if ev == nil {
			continue
		}
		iteration := result.iterations + 1
		if len(ev.CustomMetadata) != 0 {
			eventType, _ := ev.CustomMetadata[MetadataEventKey].(string)
			step, _ := ev.CustomMetadata[MetadataStepKey].(string)
			eventType = strings.TrimSpace(eventType)
			step = strings.TrimSpace(step)
			if eventType != "" && step != "" {
				switch eventType {
				case StepStarted:
					currentStep = step
					resetLatestStepOutput(&result, step)
					if err := progressEmitter.recordStepStarted(ctx, payload, step, iteration); err != nil {
						return result, err
					}
				case StepCompleted:
					if err := progressEmitter.recordStepCompleted(ctx, payload, step, iteration, stepStates[step], &deliverySeq); err != nil {
						return result, err
					}
					if step == ValidatorStep {
						result.iterations++
					}
					currentStep = ""
				case StepFailed:
					return result, fmt.Errorf("%s step failed", step)
				}
			}
		}
		if currentStep == "" {
			if ev.TurnComplete {
				sawTurnComplete = true
			}
			continue
		}
		state := stepStates[currentStep]
		if state == nil {
			state = &stepProgressState{}
			stepStates[currentStep] = state
		}
		if planSnapshot, ok := progress.ParsePlanUpdate(ev); ok {
			planText := planSnapshot.PlainText()
			if planText != "" && planText != state.lastPlanText {
				state.lastPlanText = planText
				if err := progressEmitter.recordStepPlanUpdate(ctx, payload, currentStep, iteration, planSnapshot, planText, &deliverySeq); err != nil {
					return result, err
				}
			}
		}
		text := visibleText(ev)
		if text != "" && text != state.lastVisibleText {
			state.lastVisibleText = text
			result.finalText = appendVisibleText(result.finalText, text)
			switch currentStep {
			case WorkerStep:
				result.workerOutput = appendVisibleText(result.workerOutput, text)
				result.latestWorkerOutput = appendVisibleText(result.latestWorkerOutput, text)
			case ValidatorStep:
				result.validatorOutput = appendVisibleText(result.validatorOutput, text)
				result.latestValidatorOutput = appendVisibleText(result.latestValidatorOutput, text)
			}
			if err := progressEmitter.recordStepProgress(ctx, payload, currentStep, iteration, progressKindOutput, text, &deliverySeq); err != nil {
				return result, err
			}
			state.deliveredOutput = true
		}
		if ev.TurnComplete {
			sawTurnComplete = true
		}
	}
	if result.iterations == 0 {
		result.iterations = 1
	}
	if !sawTurnComplete {
		return result, fmt.Errorf("goal workflow ended without completion")
	}
	return result, nil
}
