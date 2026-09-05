package slackagentfx

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/chatapp"
	"github.com/baldaworks/balda/internal/apps/balda/controlapp"
	"github.com/baldaworks/balda/internal/apps/balda/controlcmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	"go.uber.org/fx"
)

type inboundProcessorParams struct {
	fx.In

	Chat      chatapp.Handler
	Lifecycle slackagent.SessionLifecycle
	History   slackagent.ThreadHistoryReader
}

func newInboundProcessor(params inboundProcessorParams) slackagent.InboundProcessor {
	return slackagent.NewInboundProcessor(params.Chat, params.Lifecycle, params.History)
}

type turnCanceller struct {
	control   *controlapp.Service
	lifecycle slackagent.SessionLifecycle
}

func newTurnCanceller(control *controlapp.Service, lifecycle slackagent.SessionLifecycle) *turnCanceller {
	return &turnCanceller{control: control, lifecycle: lifecycle}
}

func (c *turnCanceller) CancelTurn(ctx context.Context, stopped slackagent.SessionStopped) error {
	if c == nil || c.control == nil {
		return actorlayer.TransientError(fmt.Errorf("control service is unavailable"))
	}
	if err := c.control.CancelSessionTurn(ctx, controlcmd.Payload{
		Action:      controlcmd.ActionCancelTurn,
		SessionID:   stopped.Locator.SessionID,
		Locator:     stopped.Locator,
		Reason:      "Slack agent session stopped by user",
		RequestedBy: stopped.RequestedBy,
		Notify:      false,
	}); err != nil {
		return err
	}
	if c.lifecycle == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	return c.lifecycle.HandleSessionStopped(ctx, stopped.Locator)
}

type boundaryObserver struct {
	lifecycle slackagent.SessionLifecycle
}

func newBoundaryObserver(lifecycle slackagent.SessionLifecycle) *boundaryObserver {
	return &boundaryObserver{lifecycle: lifecycle}
}

func (o *boundaryObserver) BeforeSessionBoundary(ctx context.Context, boundary baldasession.SessionBoundary) error {
	if boundary.Reason != baldasession.BoundaryReasonClose || strings.TrimSpace(boundary.Locator.ChannelType) != slackagent.ChannelType {
		return nil
	}
	if o == nil || o.lifecycle == nil {
		return actorlayer.TransientError(fmt.Errorf("slackagent session lifecycle is unavailable"))
	}
	return o.lifecycle.CloseSession(ctx, boundary.Locator)
}
