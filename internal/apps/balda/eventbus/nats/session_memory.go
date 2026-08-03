package natsbus

import (
	"context"
	"errors"
	"fmt"
	"time"

	gnats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
)

const sessionMemoryRetryBaseDelay = 25 * time.Millisecond

var (
	// ErrSessionMemoryQueueFull means the durable memory stream rejected new data.
	ErrSessionMemoryQueueFull = errors.New("session-memory queue is full")
	// ErrNoSessionMemoryMessages means no export arrived within the bounded fetch wait.
	ErrNoSessionMemoryMessages = errors.New("no session-memory messages available")
	// ErrSessionMemoryDisabled means the optional memory transport is disabled.
	ErrSessionMemoryDisabled = errors.New("session-memory is disabled")
	// ErrSessionMemoryConsumerUnavailable means the durable memory consumer is not configured.
	ErrSessionMemoryConsumerUnavailable = errors.New("session-memory consumer is unavailable")
)

// SessionMemoryPublishReceipt identifies one durable JetStream handoff.
type SessionMemoryPublishReceipt struct {
	Stream    string
	Sequence  uint64
	Subject   string
	ExportID  string
	Duplicate bool
}

// SessionMemoryDeliveryMetadata describes durable ordering and redelivery.
type SessionMemoryDeliveryMetadata = sessionmemoryapp.DeliveryMetadata

// SessionMemoryStats reports the durable export backlog without message bodies.
type SessionMemoryStats = sessionmemoryapp.BacklogStats

// SessionMemoryDelivery wraps one JetStream message without exposing transport types.
type SessionMemoryDelivery struct {
	msg jetstream.Msg
}

// PublishSessionMemory validates and durably publishes one export with bounded retries.
func (b *Bus) PublishSessionMemory(ctx context.Context, export sessionmemorycmd.Export) (SessionMemoryPublishReceipt, error) {
	if err := b.requireStarted(); err != nil {
		return SessionMemoryPublishReceipt{}, err
	}
	if !b.cfg.Execution.Memory.Enabled {
		return SessionMemoryPublishReceipt{}, ErrSessionMemoryDisabled
	}
	data, err := sessionmemorycmd.Marshal(export)
	if err != nil {
		return SessionMemoryPublishReceipt{}, err
	}
	subject := export.Subject()
	exportID := export.ExportID()
	publishCtx, cancel := context.WithTimeout(ctx, b.cfg.MemoryPublishTimeout)
	defer cancel()

	var lastErr error
	for attempt := 1; attempt <= b.cfg.MemoryPublishAttempts; attempt++ {
		msg := gnats.NewMsg(subject)
		msg.Data = data
		msg.Header.Set("Balda-Session-Memory-Export-ID", exportID)
		ack, publishErr := b.js.PublishMsg(
			publishCtx,
			msg,
			jetstream.WithMsgID(exportID),
			jetstream.WithExpectStream(b.cfg.Execution.Memory.Stream),
		)
		if publishErr == nil {
			return SessionMemoryPublishReceipt{
				Stream: ack.Stream, Sequence: ack.Sequence, Subject: subject,
				ExportID: exportID, Duplicate: ack.Duplicate,
			}, nil
		}
		lastErr = publishErr
		if isRuntimeQueuePressure(publishErr) {
			return SessionMemoryPublishReceipt{}, fmt.Errorf("%w: publish export %q: %w", ErrSessionMemoryQueueFull, exportID, publishErr)
		}
		if attempt == b.cfg.MemoryPublishAttempts {
			break
		}
		delay := time.Duration(attempt) * sessionMemoryRetryBaseDelay
		timer := time.NewTimer(delay)
		select {
		case <-publishCtx.Done():
			timer.Stop()
			return SessionMemoryPublishReceipt{}, fmt.Errorf("publish session-memory export %q: %w", exportID, publishCtx.Err())
		case <-timer.C:
		}
	}
	return SessionMemoryPublishReceipt{}, fmt.Errorf("publish session-memory export %q after %d attempts: %w", exportID, b.cfg.MemoryPublishAttempts, lastErr)
}

// FetchSessionMemory waits for one ordered durable export.
func (b *Bus) FetchSessionMemory(ctx context.Context) (*SessionMemoryDelivery, error) {
	if err := b.requireStarted(); err != nil {
		return nil, err
	}
	if !b.cfg.Execution.Memory.Enabled {
		return nil, ErrSessionMemoryDisabled
	}
	if b.memoryConsumer == nil {
		return nil, ErrSessionMemoryConsumerUnavailable
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	batch, err := b.memoryConsumer.Fetch(1, jetstream.FetchMaxWait(b.cfg.MemoryFetchWait))
	if err != nil {
		return nil, fmt.Errorf("fetch session-memory export: %w", err)
	}
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case msg, ok := <-batch.Messages():
		if !ok {
			if batchErr := batch.Error(); batchErr != nil && !errors.Is(batchErr, jetstream.ErrNoMessages) {
				return nil, fmt.Errorf("fetch session-memory export: %w", batchErr)
			}
			return nil, ErrNoSessionMemoryMessages
		}
		return &SessionMemoryDelivery{msg: msg}, nil
	}
}

// Export decodes and validates the delivery payload.
func (d *SessionMemoryDelivery) Export() (sessionmemorycmd.Export, error) {
	if d == nil || d.msg == nil {
		return sessionmemorycmd.Export{}, fmt.Errorf("session-memory delivery is required")
	}
	return sessionmemorycmd.Unmarshal(d.msg.Data())
}

// Subject returns the source subject without exposing the underlying message.
func (d *SessionMemoryDelivery) Subject() string {
	if d == nil || d.msg == nil {
		return ""
	}
	return d.msg.Subject()
}

// Metadata returns durable stream order and delivery-attempt counters.
func (d *SessionMemoryDelivery) Metadata() (SessionMemoryDeliveryMetadata, error) {
	if d == nil || d.msg == nil {
		return SessionMemoryDeliveryMetadata{}, fmt.Errorf("session-memory delivery is required")
	}
	metadata, err := d.msg.Metadata()
	if err != nil {
		return SessionMemoryDeliveryMetadata{}, fmt.Errorf("read session-memory delivery metadata: %w", err)
	}
	return SessionMemoryDeliveryMetadata{
		StreamSequence: metadata.Sequence.Stream, ConsumerSequence: metadata.Sequence.Consumer,
		DeliveryCount: metadata.NumDelivered,
	}, nil
}

// InProgress extends the acknowledgement deadline for the current delivery.
func (d *SessionMemoryDelivery) InProgress(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if d == nil || d.msg == nil {
		return fmt.Errorf("session-memory delivery is required")
	}
	return d.msg.InProgress()
}

// Ack durably acknowledges a successfully processed delivery.
func (d *SessionMemoryDelivery) Ack(ctx context.Context) error {
	if d == nil || d.msg == nil {
		return fmt.Errorf("session-memory delivery is required")
	}
	return d.msg.DoubleAck(ctx)
}

// Term terminates a delivery after its diagnostic has been durably recorded.
func (d *SessionMemoryDelivery) Term(reason string) error {
	if d == nil || d.msg == nil {
		return fmt.Errorf("session-memory delivery is required")
	}
	return d.msg.TermWithReason(reason)
}

// SessionMemoryStats returns stream and consumer backlog state.
func (b *Bus) SessionMemoryStats(ctx context.Context) (SessionMemoryStats, error) {
	if err := b.requireStarted(); err != nil {
		return SessionMemoryStats{}, err
	}
	if !b.cfg.Execution.Memory.Enabled {
		return SessionMemoryStats{}, ErrSessionMemoryDisabled
	}
	if b.memoryConsumer == nil {
		return SessionMemoryStats{}, ErrSessionMemoryConsumerUnavailable
	}
	stream, err := b.js.Stream(ctx, b.cfg.Execution.Memory.Stream)
	if err != nil {
		return SessionMemoryStats{}, fmt.Errorf("read session-memory stream: %w", err)
	}
	streamInfo, err := stream.Info(ctx)
	if err != nil {
		return SessionMemoryStats{}, fmt.Errorf("read session-memory stream info: %w", err)
	}
	consumerInfo, err := b.memoryConsumer.Info(ctx)
	if err != nil {
		return SessionMemoryStats{}, fmt.Errorf("read session-memory consumer info: %w", err)
	}
	stats := SessionMemoryStats{
		Messages:      streamInfo.State.Msgs,
		Pending:       consumerInfo.NumPending,
		Acknowledging: consumerInfo.NumAckPending,
	}
	if streamInfo.State.Msgs > 0 {
		stats.OldestPendingAt = streamInfo.State.FirstTime
	}
	return stats, nil
}
