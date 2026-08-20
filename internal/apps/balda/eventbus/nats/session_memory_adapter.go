package natsbus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
)

// SessionMemoryExportPublisher adapts the JetStream PubAck publisher to the
// small sessionmemoryapp handoff port. The receipt is intentionally hidden at
// this boundary; capture only needs the durability/error result.
type SessionMemoryExportPublisher struct {
	Bus *Bus
}

// Publish hands one validated export to JetStream and waits for PubAck.
func (p SessionMemoryExportPublisher) Publish(ctx context.Context, export sessionmemorycmd.Export) error {
	if p.Bus == nil {
		return fmt.Errorf("runtime transport is required")
	}
	_, err := p.Bus.PublishSessionMemory(ctx, export)
	return err
}

var _ sessionmemoryapp.ExportPublisher = SessionMemoryExportPublisher{}

// SessionMemoryExportPublisher returns the composition adapter for this bus.
func (b *Bus) SessionMemoryExportPublisher() sessionmemoryapp.ExportPublisher {
	return SessionMemoryExportPublisher{Bus: b}
}

// SessionMemoryTransport adapts the JetStream memory delivery to the
// provider-facing worker port without exposing JetStream outside this adapter.
func (b *Bus) SessionMemoryTransport() sessionmemoryapp.Transport {
	return sessionMemoryTransport{bus: b}
}

type sessionMemoryTransport struct {
	bus *Bus
}

func (t sessionMemoryTransport) Fetch(ctx context.Context) (sessionmemoryapp.Delivery, error) {
	if t.bus == nil {
		return nil, fmt.Errorf("runtime transport is required")
	}
	delivery, err := t.bus.FetchSessionMemory(ctx)
	if errors.Is(err, ErrSessionMemoryDisabled) {
		return nil, sessionmemoryapp.ErrWorkerDisabled
	}
	if errors.Is(err, ErrNoSessionMemoryMessages) {
		return nil, sessionmemoryapp.ErrNoMessages
	}
	return delivery, err
}

func (t sessionMemoryTransport) PublishDeadLetter(ctx context.Context, diagnostic sessionmemoryapp.DeadLetter) error {
	if t.bus == nil {
		return fmt.Errorf("runtime transport is required")
	}
	return t.bus.publishSessionMemoryDeadLetter(ctx, diagnostic)
}

func (t sessionMemoryTransport) Stats(ctx context.Context) (sessionmemoryapp.BacklogStats, error) {
	if t.bus == nil {
		return sessionmemoryapp.BacklogStats{}, fmt.Errorf("runtime transport is required")
	}
	return t.bus.SessionMemoryStats(ctx)
}

func (b *Bus) publishSessionMemoryDeadLetter(ctx context.Context, diagnostic sessionmemoryapp.DeadLetter) error {
	if err := b.requireStarted(); err != nil {
		return err
	}
	msgID := sessionMemoryDeadLetterID(diagnostic)
	payload, err := json.Marshal(map[string]any{
		"kind":              "session_memory_deadletter.v1",
		"export_id":         strings.TrimSpace(diagnostic.ExportID),
		"export_kind":       strings.TrimSpace(diagnostic.Kind),
		"subject":           strings.TrimSpace(diagnostic.Subject),
		"stream_sequence":   diagnostic.StreamSequence,
		"consumer_sequence": diagnostic.ConsumerSequence,
		"delivery_count":    diagnostic.DeliveryCount,
		"error_code":        strings.TrimSpace(string(diagnostic.ErrorCode)),
		"error_class":       strings.TrimSpace(string(diagnostic.ErrorClass)),
		"reason":            strings.TrimSpace(diagnostic.Reason),
		"source_stream":     b.cfg.Execution.Memory.Stream,
		"source_consumer":   b.cfg.Execution.Memory.Consumer,
	})
	if err != nil {
		return fmt.Errorf("marshal session-memory dead letter: %w", err)
	}
	env := actorlayer.Envelope{
		ID:        msgID,
		Namespace: baldaexecution.NamespaceTelemetry,
		Kind:      "session_memory_deadletter",
		From:      actorlayer.SystemAddress("session-memory"),
		To:        actorlayer.SystemAddress("dlq"),
		DedupeKey: msgID,
		Payload: actorlayer.Payload{
			Encoding: actorlayer.EncodingJSON,
			Data:     payload,
		},
	}
	msg, err := messageFromEnvelope(baldaexecution.SubjectDLQCommand, env)
	if err != nil {
		return err
	}
	msg.Header.Set("Balda-DLQ-Reason", strings.TrimSpace(diagnostic.Reason))
	msg.Header.Set("Balda-DLQ-Source-Stream", b.cfg.Execution.Memory.Stream)
	msg.Header.Set("Balda-DLQ-Source-Consumer", b.cfg.Execution.Memory.Consumer)
	if subject := strings.TrimSpace(diagnostic.Subject); subject != "" {
		msg.Header.Set("Balda-DLQ-Source-Subject", subject)
	}
	if diagnostic.DeliveryCount > 0 {
		msg.Header.Set("Balda-DLQ-Num-Delivered", fmt.Sprintf("%d", diagnostic.DeliveryCount))
	}
	if value := strings.TrimSpace(string(diagnostic.ErrorCode)); value != "" {
		msg.Header.Set("Balda-DLQ-Error-Code", value)
	}
	if value := strings.TrimSpace(string(diagnostic.ErrorClass)); value != "" {
		msg.Header.Set("Balda-DLQ-Error-Class", value)
	}
	if _, err := b.js.PublishMsg(ctx, msg, jetstream.WithExpectStream(b.cfg.Execution.DLQ.Stream), jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("publish session-memory dead letter: %w", err)
	}
	return nil
}

func sessionMemoryDeadLetterID(diagnostic sessionmemoryapp.DeadLetter) string {
	if exportID := strings.TrimSpace(diagnostic.ExportID); exportID != "" {
		return "session-memory:" + exportID + ":dlq"
	}
	if diagnostic.StreamSequence > 0 {
		return fmt.Sprintf("session-memory:%s:%d:dlq", strings.TrimSpace(diagnostic.Subject), diagnostic.StreamSequence)
	}
	return "session-memory:unknown:" + uuid.NewString() + ":dlq"
}
