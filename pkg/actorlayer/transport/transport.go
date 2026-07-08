package transport

import (
	"context"

	"github.com/normahq/balda/pkg/actorlayer"
)

// DispatchReceipt describes the transport result of a dispatch operation.
type DispatchReceipt struct {
	Stream    string
	Sequence  uint64
	Subject   string
	MsgID     string
	Duplicate bool
}

// Dispatcher sends actor envelopes to a command transport.
type Dispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*DispatchReceipt, error)
}

// EventPublisher publishes actorlayer event envelopes.
type EventPublisher interface {
	PublishEvent(ctx context.Context, subject string, env actorlayer.Envelope) error
}

// EventHandler handles one event envelope from a subject.
type EventHandler func(ctx context.Context, subject string, env actorlayer.Envelope) error

// EventConsumer streams event envelopes to a handler.
type EventConsumer interface {
	RunEventConsumer(ctx context.Context, handler EventHandler) error
}

// Drainer stops a transport from accepting new work and lets readers exit.
type Drainer interface {
	Drain(ctx context.Context) error
}
