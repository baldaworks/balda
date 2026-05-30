package swarm

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// ErrCommandQueueFull means the durable command stream rejected new work due to pressure.
var ErrCommandQueueFull = errors.New("command queue is full")

// IsCommandQueueFull reports whether an error came from command stream pressure.
func IsCommandQueueFull(err error) bool {
	return errors.Is(err, ErrCommandQueueFull)
}

// DispatchReceipt is the durable acceptance receipt for a dispatched actor envelope.
type DispatchReceipt struct {
	Stream    string
	Sequence  uint64
	Subject   string
	MsgID     string
	Duplicate bool
}

// ActorDispatcher dispatches durable actor envelopes into the actorlayer runtime.
type ActorDispatcher interface {
	Dispatch(ctx context.Context, env Envelope) (*DispatchReceipt, error)
}

// EventPublisher publishes durable telemetry events.
type EventPublisher interface {
	PublishEvent(ctx context.Context, subject string, env Envelope) error
}

// BusDrainer drains transport resources.
type BusDrainer interface {
	Drain(ctx context.Context) error
}

// EventHandler is kept for event projector code that consumes decoded events.
type EventHandler func(ctx context.Context, subject string, env Envelope) error

// EventConsumer consumes durable JetStream events for read-model projection.
type EventConsumer interface {
	RunEventConsumer(ctx context.Context, handler EventHandler) error
}

// RetryDelay computes the first retry delay for simple bus adapters.
func RetryDelay(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := retryBaseDelay
	for range attempt {
		delay *= 2
		if delay >= retryMaxDelay {
			delay = retryMaxDelay
			break
		}
	}
	jitterCap := max(delay/4, time.Millisecond)
	jitter := time.Duration(rand.Int64N(int64(jitterCap)))
	return delay + jitter
}

// RetryExhausted reports whether an attempt has reached terminal retry limit.
func RetryExhausted(attempt int, maxAttempts int) bool {
	return maxAttempts > 0 && attempt >= maxAttempts
}

const (
	retryBaseDelay = time.Second
	retryMaxDelay  = time.Minute
)
