package sessionturnapp

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

func dispatchOutbound(ctx context.Context, dispatcher actortransport.Dispatcher, env actorlayer.Envelope) error {
	if dispatcher == nil {
		return fmt.Errorf("runtime is unavailable")
	}
	_, err := dispatcher.Dispatch(ctx, env)
	return err
}

func sendProgressActivity(ctx context.Context, dispatcher actortransport.Dispatcher, jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, sequence int, dedupeSuffix string) error {
	env, err := deliverycmd.ProgressActivityEnvelope(jobID, from, locator, deliveryProgressPolicy(policy), sequence, dedupeSuffix)
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendProgressThinking(ctx context.Context, dispatcher actortransport.Dispatcher, jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, text string, sequence int, dedupeSuffix string) error {
	env, err := deliverycmd.ProgressThinkingEnvelope(jobID, from, locator, deliveryProgressPolicy(policy), visible, text, sequence, dedupeSuffix)
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func sendProgressPlanUpdate(ctx context.Context, dispatcher actortransport.Dispatcher, jobID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, plan *progress.PlanSnapshot, text string, dedupeSuffix string) error {
	env, err := deliverycmd.ProgressPlanUpdateEnvelope(jobID, from, locator, deliveryProgressPolicy(policy), visible, deliveryPlanSnapshot(plan), text, dedupeSuffix)
	if err != nil {
		return err
	}
	return dispatchOutbound(ctx, dispatcher, env)
}

func deliveryProgressPolicy(policy deliveryfmt.ProgressPolicy) deliverycmd.ProgressPolicy {
	return deliverycmd.ProgressPolicy{
		Typing:      policy.Typing,
		Thinking:    policy.Thinking,
		PlanUpdates: policy.PlanUpdates,
	}
}

func deliveryPlanSnapshot(plan *progress.PlanSnapshot) *deliverycmd.PlanSnapshot {
	if plan == nil {
		return nil
	}
	out := &deliverycmd.PlanSnapshot{Entries: make([]deliverycmd.PlanEntry, 0, len(plan.Entries))}
	for _, entry := range plan.Entries {
		out.Entries = append(out.Entries, deliverycmd.PlanEntry{
			Content: entry.Content,
			Status:  entry.Status,
		})
	}
	return out
}
