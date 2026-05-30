package handlers

import (
	"context"
	"fmt"

	"github.com/normahq/balda/internal/apps/balda/actors"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

func submitSessionCancelControl(
	ctx context.Context,
	coordinator *swarm.Coordinator,
	locator baldasession.SessionLocator,
	requestedBy string,
	reason string,
	notify bool,
) error {
	if coordinator == nil || !coordinator.RuntimeEnabled() {
		return nil
	}
	env, err := actors.ControlCancelEnvelopeWithNotify(locator, "", requestedBy, reason, notify)
	if err != nil {
		return fmt.Errorf("build session cancel control envelope: %w", err)
	}
	if _, err := coordinator.Dispatch(ctx, env); err != nil {
		return fmt.Errorf("publish session cancel control command: %w", err)
	}
	return nil
}
