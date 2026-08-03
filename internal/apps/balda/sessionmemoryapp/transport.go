package sessionmemoryapp

import (
	"context"
	"errors"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
)

var (
	// ErrNoMessages tells a worker that the bounded fetch wait elapsed.
	ErrNoMessages = errors.New("no session-memory messages available")
	// ErrWorkerDisabled means the optional session-memory provider is disabled.
	ErrWorkerDisabled = errors.New("session-memory worker is disabled")
	// ErrWorkerStarted means a worker cannot be started twice concurrently.
	ErrWorkerStarted = errors.New("session-memory worker is already started")
)

// DeliveryMetadata describes durable order and redelivery without exposing a
// concrete queue client.
type DeliveryMetadata struct {
	StreamSequence   uint64
	ConsumerSequence uint64
	DeliveryCount    uint64
}

// Delivery is one unresolved, durable session-memory export.
type Delivery interface {
	Export() (sessionmemorycmd.Export, error)
	Subject() string
	Metadata() (DeliveryMetadata, error)
	InProgress(ctx context.Context) error
	Ack(ctx context.Context) error
	Term(reason string) error
}

// BacklogStats reports durable session-memory work without message bodies.
type BacklogStats struct {
	Messages        uint64
	Pending         uint64
	Acknowledging   int
	OldestPendingAt time.Time
}

// DeadLetter is a redacted diagnostic. Export text and provider response
// bodies must never cross this boundary.
type DeadLetter struct {
	ExportID         string
	Kind             string
	Subject          string
	StreamSequence   uint64
	ConsumerSequence uint64
	DeliveryCount    uint64
	ErrorCode        sessionmemory.ErrorCode
	ErrorClass       sessionmemory.ErrorClass
	Reason           string
}

// Transport is the queue port consumed by the serialized worker.
type Transport interface {
	Fetch(ctx context.Context) (Delivery, error)
	PublishDeadLetter(ctx context.Context, diagnostic DeadLetter) error
	Stats(ctx context.Context) (BacklogStats, error)
}
