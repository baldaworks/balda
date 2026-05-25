package swarm

import (
	"context"
	"fmt"
	"time"
)

// CommandPublishResult is the JetStream acknowledgement for an accepted command.
type CommandPublishResult struct {
	Stream    string
	Sequence  uint64
	Subject   string
	MsgID     string
	Duplicate bool
}

// CommandMessage is a command delivered by the durable command bus.
type CommandMessage interface {
	Envelope() Envelope
	Subject() string
	InProgress(ctx context.Context) error
	DeliveryAttempt() int
	MaxDeliveries() int
}

// CommandHandler handles one durable command message.
type CommandHandler func(ctx context.Context, msg CommandMessage) error

// CommandBus is Balda's transport contract. JetStream is the only runtime implementation.
type CommandBus interface {
	PublishCommand(ctx context.Context, env Envelope) (*CommandPublishResult, error)
	PublishEvent(ctx context.Context, subject string, env Envelope) error
	PublishDLQ(ctx context.Context, env Envelope, reason string) error
	RunCommandConsumer(ctx context.Context, handler CommandHandler) error
	Drain(ctx context.Context) error
}

// CommandBusStatus describes JetStream stream and consumer state for /swarm status.
type CommandBusStatus struct {
	CommandBus       string
	SQLiteCommandBus bool
	ShadowMode       bool
	LegacyDirectPath bool
	Embedded         bool
	Running          bool
	JetStream        bool
	ClientURL        string
	Commands         StreamStatus
	Events           StreamStatus
	DLQ              StreamStatus
	Worker           ConsumerStatus
	ProjectionLag    map[string]uint64
}

// StreamStatus contains compact JetStream stream metadata.
type StreamStatus struct {
	Name     string
	Messages uint64
	Bytes    uint64
	FirstSeq uint64
	LastSeq  uint64
}

// ConsumerStatus contains compact JetStream consumer metadata.
type ConsumerStatus struct {
	Name           string
	NumPending     uint64
	NumAckPending  int
	NumRedelivered uint64
	NumWaiting     int
	DeliveredSeq   uint64
	AckFloorSeq    uint64
}

// CommandBusStatusProvider is implemented by buses that can report runtime status.
type CommandBusStatusProvider interface {
	Status(ctx context.Context) (CommandBusStatus, error)
}

// UnsupportedCommandBus is installed when the swarm runtime is disabled.
type UnsupportedCommandBus struct{}

func (UnsupportedCommandBus) PublishCommand(context.Context, Envelope) (*CommandPublishResult, error) {
	return nil, fmt.Errorf("command bus is unavailable")
}

func (UnsupportedCommandBus) PublishEvent(context.Context, string, Envelope) error { return nil }

func (UnsupportedCommandBus) PublishDLQ(context.Context, Envelope, string) error { return nil }

func (UnsupportedCommandBus) RunCommandConsumer(ctx context.Context, _ CommandHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (UnsupportedCommandBus) Drain(context.Context) error { return nil }

func (UnsupportedCommandBus) Status(context.Context) (CommandBusStatus, error) {
	return CommandBusStatus{CommandBus: "unavailable", SQLiteCommandBus: false, ShadowMode: false, LegacyDirectPath: false}, nil
}

// EventHandler is kept for event projector code that consumes decoded events.
type EventHandler func(ctx context.Context, subject string, env Envelope) error

// EventConsumer consumes durable JetStream events for read-model projection.
type EventConsumer interface {
	RunEventConsumer(ctx context.Context, handler EventHandler) error
}

// Subscription is a cancellable event subscription.
type Subscription interface {
	Unsubscribe() error
}

// RetryDelay computes the first retry delay for simple bus adapters.
func RetryDelay(attempt int) time.Duration {
	return nextRetryDelay(attempt)
}
