package controlapp

import (
	"context"
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// CommandDispatcher dispatches turn-cancel and goal-clear control envelopes.
type CommandDispatcher struct {
	dispatcher actortransport.Dispatcher
}

// NewCommandDispatcher creates a new CommandDispatcher.
func NewCommandDispatcher(dispatcher actortransport.Dispatcher) *CommandDispatcher {
	return &CommandDispatcher{dispatcher: dispatcher}
}

// CancelTurn dispatches a turn cancellation control envelope.
func (d *CommandDispatcher) CancelTurn(ctx context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	if d == nil || d.dispatcher == nil {
		return fmt.Errorf("dispatcher is unavailable")
	}
	env, err := controlcmd.CancelTurnEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return err
	}
	_, err = d.dispatcher.Dispatch(ctx, env)
	return err
}

// ClearGoal dispatches a goal clear control envelope.
func (d *CommandDispatcher) ClearGoal(ctx context.Context, locator deliverycmd.Locator, requestedBy string, reason string, notify bool) error {
	if d == nil || d.dispatcher == nil {
		return fmt.Errorf("dispatcher is unavailable")
	}
	env, err := controlcmd.ClearGoalEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return err
	}
	_, err = d.dispatcher.Dispatch(ctx, env)
	return err
}
