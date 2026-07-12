package goalkeeper

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/goaldelivery"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
)

// progress_emitter.go owns goal progress side effects: delivery and job events.
type goalProgressEmitter struct {
	jobs       jobLifecycle
	events     jobEvents
	dispatcher actortransport.Dispatcher
}

func newGoalProgressEmitter(jobs jobLifecycle, events jobEvents, dispatcher actortransport.Dispatcher) goalProgressEmitter {
	return goalProgressEmitter{
		jobs:       jobs,
		events:     events,
		dispatcher: dispatcher,
	}
}

func (e goalProgressEmitter) recordStepStarted(ctx context.Context, payload goalJobPayload, step string, iteration int) error {
	status := baldastate.JobStatusWaitingForAgent
	if step == ValidatorStep {
		status = baldastate.JobStatusValidating
	}
	if err := e.jobs.MarkStatus(ctx, payload.JobID, status, actorName, "", "", map[string]any{
		"step":      step,
		"iteration": iteration,
	}); err != nil {
		return actorlayer.TransientError(err)
	}
	if err := e.events.AppendEvent(ctx, payload.JobID, baldajobs.JobEventAgentStarted, actorName, "", map[string]any{
		"step":      step,
		"iteration": iteration,
	}); err != nil {
		return actorlayer.TransientError(err)
	}
	return e.deliver(ctx, payload.JobID, payload, goaldelivery.RenderStepMessage(goalDeliveryProfile(payload), iteration, normalizeGoalMaxIterations(payload.MaxIterations), step, "started", ""), "started:"+step+":"+strconv.Itoa(iteration))
}

func (e goalProgressEmitter) recordStepCompleted(
	ctx context.Context,
	payload goalJobPayload,
	step string,
	iteration int,
	state *stepProgressState,
	deliverySeq *int,
) error {
	text := ""
	if state != nil && !state.deliveredOutput {
		text = state.lastVisibleText
	}
	if err := e.recordGoalProgress(ctx, newGoalProgressUpdate(
		payload,
		step,
		iteration,
		deliverycmd.GoalProgressKindCompleted,
		text,
		nil,
		nextDeliverySequence(deliverySeq),
	)); err != nil {
		return err
	}
	if err := e.events.AppendEvent(ctx, payload.JobID, baldajobs.JobEventAgentResult, actorName, "", map[string]any{
		"step":      step,
		"iteration": normalizeGoalIteration(iteration),
	}); err != nil {
		return actorlayer.TransientError(err)
	}
	return nil
}

func (e goalProgressEmitter) recordStepPlanUpdate(
	ctx context.Context,
	payload goalJobPayload,
	step string,
	iteration int,
	plan progress.PlanSnapshot,
	text string,
	deliverySeq *int,
) error {
	return e.recordGoalProgress(ctx, newGoalProgressUpdate(
		payload,
		step,
		iteration,
		deliverycmd.GoalProgressKindPlan,
		text,
		&plan,
		nextDeliverySequence(deliverySeq),
	))
}

func (e goalProgressEmitter) recordStepProgress(
	ctx context.Context,
	payload goalJobPayload,
	step string,
	iteration int,
	kind string,
	text string,
	deliverySeq *int,
) error {
	return e.recordGoalProgress(ctx, newGoalProgressUpdate(
		payload,
		step,
		iteration,
		deliverycmd.GoalProgressKind(strings.TrimSpace(kind)),
		text,
		nil,
		nextDeliverySequence(deliverySeq),
	))
}

func (e goalProgressEmitter) recordGoalProgress(ctx context.Context, update deliverycmd.GoalProgressUpdate) error {
	switch update.Kind {
	case deliverycmd.GoalProgressKindOutput, deliverycmd.GoalProgressKindCompleted:
		update.Text = renderGoalProgressText(update)
	}
	if err := dispatchGoalProgress(ctx, e.dispatcher, update); err != nil {
		return err
	}
	if err := e.events.AppendEvent(ctx, update.JobID, baldajobs.JobEventAgentProgress, actorName, "", goalProgressEventPayload(update)); err != nil {
		return actorlayer.TransientError(err)
	}
	return nil
}

func (e goalProgressEmitter) deliver(
	ctx context.Context,
	jobID string,
	payload goalJobPayload,
	text string,
	dedupeSuffix string,
) error {
	if e.dispatcher == nil {
		return actorlayer.TransientError(fmt.Errorf("actor dispatcher is required"))
	}
	message := redactSecrets(strings.TrimSpace(text))
	if message == "" {
		return nil
	}
	env, err := deliverycmd.AgentReplyEnvelopeWithProfile(
		strings.TrimSpace(jobID),
		actorlayer.ActorAddress{Target: baldaexecution.ActorTypeGoalkeeper, Key: jobID},
		normalizeGoalDeliveryLocator(payload.Locator),
		goalDeliveryProfile(payload),
		message,
		dedupeSuffix,
	)
	if err != nil {
		return actorlayer.PermanentError(fmt.Errorf("build goal delivery envelope: %w", err))
	}
	if _, err := e.dispatcher.Dispatch(ctx, env); err != nil {
		return actorlayer.TransientError(err)
	}
	return nil
}
