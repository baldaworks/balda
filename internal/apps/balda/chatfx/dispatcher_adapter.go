package chatfx

import (
	"context"
	"fmt"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

// DispatcherAdapter adapts actortransport.Dispatcher to chatapp.Dispatcher.
type DispatcherAdapter struct {
	dispatcher actortransport.Dispatcher
}

// NewDispatcherAdapter constructs a DispatcherAdapter.
func NewDispatcherAdapter(dispatcher actortransport.Dispatcher) *DispatcherAdapter {
	return &DispatcherAdapter{dispatcher: dispatcher}
}

// Dispatch dispatches an envelope to the actor transport.
func (a *DispatcherAdapter) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	if a == nil || a.dispatcher == nil {
		return nil, actorlayer.TransientError(fmt.Errorf("runtime is unavailable"))
	}
	return a.dispatcher.Dispatch(ctx, env)
}
