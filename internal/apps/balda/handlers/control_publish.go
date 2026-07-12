package handlers

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/controlcmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

func submitSessionCancelControl(
	ctx context.Context,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	requestedBy string,
	reason string,
	notify bool,
) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelEnvelopeWithNotify(locator, "", requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session cancel control command: %w", err)
	}
	return nil
}

func submitSessionTurnCancelControl(
	ctx context.Context,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	requestedBy string,
	reason string,
	notify bool,
) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.CancelTurnEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session turn cancel control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session turn cancel control command: %w", err)
	}
	return nil
}

func submitGoalClearControl(
	ctx context.Context,
	dispatcher actortransport.Dispatcher,
	locator baldasession.SessionLocator,
	requestedBy string,
	reason string,
	notify bool,
) error {
	if dispatcher == nil {
		return nil
	}
	env, err := controlcmd.ClearGoalEnvelopeWithNotify(locator, requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build goal clear control envelope: %w", err)
	}
	if _, err := dispatcher.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish goal clear control command: %w", err)
	}
	return nil
}
