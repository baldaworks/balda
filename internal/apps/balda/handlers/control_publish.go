package handlers

import (
	"context"
	"fmt"

	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
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
