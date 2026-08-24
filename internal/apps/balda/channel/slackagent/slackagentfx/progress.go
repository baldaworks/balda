package slackagentfx

import (
	"context"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/sessionturnapp"
)

type progressTransportHook struct{}

func (progressTransportHook) DispatchVisibleResponse(ctx context.Context, req sessionturnapp.VisibleResponseRequest) (bool, error) {
	if strings.TrimSpace(req.Locator.ChannelType) != slackagent.ChannelType {
		return false, nil
	}
	if strings.TrimSpace(req.VisibleResponseText) == "" {
		return false, nil
	}
	if err := req.Dispatch(ctx, req.VisibleResponseText); err != nil {
		return true, err
	}
	return true, nil
}
