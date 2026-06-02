package swarm

import (
	"errors"
	"time"

	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
)

// ErrCommandQueueFull means the durable command stream rejected new work due to pressure.
var ErrCommandQueueFull = errors.New("command queue is full")

// IsCommandQueueFull reports whether an error came from command stream pressure.
func IsCommandQueueFull(err error) bool {
	return errors.Is(err, ErrCommandQueueFull)
}

// DispatchReceipt is the durable acceptance receipt for a dispatched actor envelope.
type DispatchReceipt = actortransport.DispatchReceipt

// ActorDispatcher dispatches durable actor envelopes into the actorlayer runtime.
type ActorDispatcher = actortransport.Dispatcher

// EventPublisher publishes durable telemetry events.
type EventPublisher = actortransport.EventPublisher

// BusDrainer drains transport resources.
type BusDrainer = actortransport.Drainer

// EventHandler is kept for event projector code that consumes decoded events.
type EventHandler = actortransport.EventHandler

// EventConsumer consumes durable runtime events for read-model projection.
type EventConsumer = actortransport.EventConsumer

// RetryDelay computes the first retry delay for simple bus adapters.
func RetryDelay(attempt int) time.Duration {
	return actorlayer.RetryDelay(attempt)
}

// RetryExhausted reports whether an attempt has reached terminal retry limit.
func RetryExhausted(attempt int, maxAttempts int) bool {
	return actorlayer.RetryExhausted(attempt, maxAttempts)
}
