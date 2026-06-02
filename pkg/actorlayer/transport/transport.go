package transport

import (
	"context"

	"github.com/normahq/balda/pkg/actorlayer"
)

type DispatchReceipt struct {
	Stream    string
	Sequence  uint64
	Subject   string
	MsgID     string
	Duplicate bool
}

type Dispatcher interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*DispatchReceipt, error)
}

type EventPublisher interface {
	PublishEvent(ctx context.Context, subject string, env actorlayer.Envelope) error
}

type EventHandler func(ctx context.Context, subject string, env actorlayer.Envelope) error

type EventConsumer interface {
	RunEventConsumer(ctx context.Context, handler EventHandler) error
}

type Drainer interface {
	Drain(ctx context.Context) error
}
