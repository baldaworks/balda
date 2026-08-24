package sessionturnapp

import (
	"context"
	"strings"

	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
)

type ProgressTransportHook interface {
	DispatchVisibleResponse(ctx context.Context, req VisibleResponseRequest) (bool, error)
}

type VisibleResponseRequest struct {
	Locator             baldasession.SessionLocator
	VisibleResponseText string
	Dispatch            func(context.Context, string) error
}

type noopProgressTransportHook struct{}

func (noopProgressTransportHook) DispatchVisibleResponse(_ context.Context, req VisibleResponseRequest) (bool, error) {
	if strings.TrimSpace(req.VisibleResponseText) == "" {
		return false, nil
	}
	return false, nil
}
