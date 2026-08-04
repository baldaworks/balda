package natsbus

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actorengine "github.com/baldaworks/go-actorlayer/engine"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/google/uuid"
	"github.com/nats-io/nats.go/jetstream"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/rs/zerolog"
)

const commandSettlementTimeout = 5 * time.Second
const unknownDecodeTarget = "unknown"
const settlementStageTerminal = "terminal"
const settlementTerminalUnclassified = settlementStageTerminal + ":unclassified"

const (
	dlqMetaErrorClass   = "dlq_error_class"
	dlqMetaSourceStream = "dlq_source_stream"
	dlqMetaSourceCns    = "dlq_source_consumer"
	dlqMetaSourceSubj   = "dlq_source_subject"
	dlqMetaDelivered    = "dlq_num_delivered"
)

type commandMessage struct {
	subject       string
	env           actorlayer.Envelope
	msg           jetstream.Msg
	numDelivered  int
	maxDeliveries int
	bus           *Bus

	mu      sync.Mutex
	settled bool
}

func (m *commandMessage) Envelope() actorengine.Envelope { return m.env }
func (m *commandMessage) InProgress(context.Context) error {
	return m.msg.InProgress()
}
func (m *commandMessage) Attempt() int     { return m.numDelivered }
func (m *commandMessage) MaxAttempts() int { return m.maxDeliveries }

func (m *commandMessage) Ack(ctx context.Context) error {
	return m.settle(func() error {
		settleCtx, settleCancel := settlementContext(ctx)
		defer settleCancel()
		if err := m.msg.DoubleAck(settleCtx); err != nil {
			return err
		}
		if err := m.bus.PublishEvent(settleCtx, baldaexecution.SubjectEventCommandAcked, commandEventEnvelope(m.env, nil, "acked", "", nil)); err != nil {
			m.bus.logger.Warn().
				Err(err).
				Str("envelope_id", m.env.ID).
				Msg("failed to publish command acked event")
		}
		commandLogEnvelope(commandLogEvent(m.bus.logger.Info(), m.msg), m.env).Msg("command handled and acknowledged")
		return nil
	})
}

func (m *commandMessage) Retry(ctx context.Context, delay time.Duration, reason string) error {
	return m.settle(func() error {
		safeReason := sanitizeReasonForStage(reason, "retrying")
		settleCtx, settleCancel := settlementContext(ctx)
		defer settleCancel()
		if err := m.msg.NakWithDelay(delay); err != nil {
			return err
		}
		eventExtras := map[string]any{
			"retry_delay_ms":  delay.Milliseconds(),
			"next_attempt_at": time.Now().UTC().Add(delay).Format(time.RFC3339Nano),
		}
		if err := m.bus.PublishEvent(settleCtx, baldaexecution.SubjectEventCommandRetrying, commandEventEnvelope(m.env, nil, "retrying", safeReason, eventExtras)); err != nil {
			m.bus.logger.Warn().
				Err(err).
				Str("envelope_id", m.env.ID).
				Msg("failed to publish command retrying event")
		}
		commandLogEnvelope(commandLogEvent(m.bus.logger.Warn(), m.msg), m.env).
			Str("settlement_class", safeReason).
			Dur("retry_delay", delay).
			Msg("command failed with retryable error")
		return nil
	})
}

func (m *commandMessage) DeadLetter(ctx context.Context, reason string) error {
	return m.settle(func() error {
		safeReason := sanitizeReason(reason)
		settleCtx, settleCancel := settlementContext(ctx)
		defer settleCancel()
		if err := m.bus.publishDLQ(settleCtx, m.env, safeReason, false); err != nil {
			return err
		}
		if err := m.msg.TermWithReason(safeReason); err != nil {
			return err
		}
		m.bus.publishCommandEventBestEffort(settleCtx, baldaexecution.SubjectEventCommandDeadLettered, m.env, "deadlettered", safeReason)
		commandLogEnvelope(commandLogEvent(m.bus.logger.Warn(), m.msg), m.env).
			Str("settlement_class", safeReason).
			Msg("command deadlettered")
		return nil
	})
}

func (m *commandMessage) isSettled() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.settled
}

func (m *commandMessage) settle(fn func() error) error {
	m.mu.Lock()
	if m.settled {
		m.mu.Unlock()
		return nil
	}
	m.mu.Unlock()
	if err := fn(); err != nil {
		return err
	}
	m.mu.Lock()
	m.settled = true
	m.mu.Unlock()
	return nil
}

func (b *Bus) Run(ctx context.Context, handler actorengine.Handler) error {
	if err := b.requireStarted(); err != nil {
		return err
	}
	if b == nil || b.consumer == nil {
		return fmt.Errorf("actor delivery consumer is required")
	}
	workerLimit := b.commandWorkerLimit()
	workers := make(chan struct{}, workerLimit)
	var wg sync.WaitGroup
	defer wg.Wait()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		available := workerLimit - len(workers)
		if available <= 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		fetchSize := b.cfg.Execution.Commands.FetchBatch
		if fetchSize <= 0 {
			fetchSize = 1
		}
		if fetchSize > available {
			fetchSize = available
		}
		batch, err := b.consumer.Fetch(fetchSize, jetstream.FetchMaxWait(b.cfg.FetchWait))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for msg := range batch.Messages() {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case workers <- struct{}{}:
			}
			wg.Add(1)
			go func(msg jetstream.Msg) {
				defer wg.Done()
				defer func() { <-workers }()
				if err := b.handleMessage(ctx, msg, handler); err != nil {
					b.logger.Warn().Err(err).Str("subject", msg.Subject()).Msg("failed to settle command")
				}
			}(msg)
		}
	}
}

func (b *Bus) commandWorkerLimit() int {
	if b == nil {
		return 1
	}
	switch {
	case b.cfg.Execution.Commands.FetchBatch > 0:
		// Keep local in-memory fan-out bounded to the pull batch size.
		// Transport max_ack_pending stays the transport limit.
		return b.cfg.Execution.Commands.FetchBatch
	case b.cfg.Execution.Commands.MaxAckPending > 0:
		return b.cfg.Execution.Commands.MaxAckPending
	default:
		return 1
	}
}

func (b *Bus) handleMessage(ctx context.Context, msg jetstream.Msg, handler actorengine.Handler) error {
	env, err := actorlayer.DecodeEnvelope(string(msg.Data()))
	if err != nil {
		id := strings.TrimSpace(msg.Headers().Get(baldaexecution.HeaderEnvelopeID))
		if id == "" {
			id = "poison-" + uuid.NewString()
		}
		namespace := strings.TrimSpace(msg.Headers().Get(baldaexecution.HeaderNamespace))
		if namespace == "" {
			namespace = baldaexecution.NamespaceTelemetry
		}
		toTarget, toKey := unknownDecodeTarget, unknownDecodeTarget
		if strings.HasPrefix(msg.Subject(), "balda.v1.cmd.") {
			toTarget = strings.TrimPrefix(msg.Subject(), "balda.v1.cmd.")
			toKey = strings.TrimSpace(msg.Headers().Get(baldaexecution.HeaderActorKey))
			if toKey == "" {
				toKey = unknownDecodeTarget
			}
		}
		payload, _ := json.Marshal(payloadDiagnostics(msg.Data(), "decode_failed", ""))
		decodeFailureEnv := actorlayer.Envelope{
			ID:        id,
			Namespace: namespace,
			Kind:      "decode_failed",
			From:      actorlayer.SystemAddress("transport"),
			To:        actorlayer.ActorAddress{Target: toTarget, Key: toKey},
			Payload: actorlayer.Payload{
				Encoding: actorlayer.EncodingJSON,
				Data:     payload,
			},
		}
		settleCtx, settleCancel := settlementContext(ctx)
		defer settleCancel()
		_ = b.publishRawDLQ(settleCtx, msg, "decode_failed")
		b.publishCommandEventBestEffort(settleCtx, baldaexecution.SubjectEventCommandDecodeFailed, decodeFailureEnv, "decode_failed", "decode_failed")
		_ = msg.TermWithReason("decode_failed")
		commandLogEvent(b.logger.Warn(), msg).
			Str("settlement_class", "decode_failed").
			Msg("failed to decode command envelope; moved to dlq")
		return err
	}
	numDelivered := messageDeliveryAttempt(msg)
	env.Attempt = numDelivered - 1
	cmd := &commandMessage{
		subject:       msg.Subject(),
		env:           env,
		msg:           msg,
		numDelivered:  numDelivered,
		maxDeliveries: b.cfg.Execution.Commands.MaxDeliver,
		bus:           b,
	}
	b.publishCommandEventBestEffort(ctx, baldaexecution.SubjectEventCommandRunning, env, "running", "")
	commandLogEnvelope(commandLogEvent(b.logger.Debug(), msg), env).Msg("command running")
	err = handler(ctx, cmd)
	if cmd.isSettled() {
		return nil
	}
	settleCtx, settleCancel := settlementContext(ctx)
	defer settleCancel()
	if err == nil {
		return cmd.Ack(settleCtx)
	}
	if actorlayer.IsRetryableError(err) {
		if actorlayer.RetryExhausted(numDelivered, b.cfg.Execution.Commands.MaxDeliver) {
			reason := safeReason("retry_exhausted", actorlayer.ClassifyError(err))
			cmd.env = decorateDLQEnvelope(cmd.env, reason, actorlayer.ClassifyError(err), b.cfg.Execution.Commands.Stream, b.cfg.Execution.Commands.Consumer, msg.Subject(), numDelivered)
			return cmd.DeadLetter(settleCtx, reason)
		}
		delay := actorlayer.RetryDelay(env.Attempt)
		return cmd.Retry(settleCtx, delay, safeReason("retrying", actorlayer.ClassifyError(err)))
	}
	reason := safeReason(settlementStageTerminal, actorlayer.ClassifyError(err))
	cmd.env = decorateDLQEnvelope(cmd.env, reason, actorlayer.ClassifyError(err), b.cfg.Execution.Commands.Stream, b.cfg.Execution.Commands.Consumer, msg.Subject(), numDelivered)
	return cmd.DeadLetter(settleCtx, reason)
}

func settlementContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), commandSettlementTimeout)
}

func (b *Bus) publishCommandEventBestEffort(ctx context.Context, subject string, env actorlayer.Envelope, status string, reason string) {
	if err := b.PublishEvent(ctx, subject, commandEventEnvelope(env, nil, status, reason, nil)); err != nil {
		b.logger.Warn().
			Err(err).
			Str("envelope_id", env.ID).
			Str("event_status", status).
			Msg("failed to publish command lifecycle event")
	}
}

func (b *Bus) RunEventConsumer(ctx context.Context, handler actortransport.EventHandler) error {
	if err := b.requireStarted(); err != nil {
		return err
	}
	if b == nil || b.eventConsumer == nil {
		return fmt.Errorf("event projector consumer is required")
	}
	if handler == nil {
		return fmt.Errorf("event handler is required")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		batch, err := b.eventConsumer.Fetch(b.cfg.Execution.Commands.FetchBatch, jetstream.FetchMaxWait(b.cfg.FetchWait))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for msg := range batch.Messages() {
			if err := b.handleEventMessage(ctx, msg, handler); err != nil {
				b.logger.Warn().Err(err).Str("subject", msg.Subject()).Msg("failed to settle event")
			}
		}
	}
}

func (b *Bus) handleEventMessage(ctx context.Context, msg jetstream.Msg, handler actortransport.EventHandler) error {
	env, err := actorlayer.DecodeEnvelope(string(msg.Data()))
	if err != nil {
		_ = b.publishRawDLQ(ctx, msg, "decode_failed")
		_ = msg.TermWithReason("decode_failed")
		return err
	}
	if err := handler(ctx, msg.Subject(), env); err != nil {
		numDelivered := messageDeliveryAttempt(msg)
		if actorlayer.IsRetryableError(err) && !actorlayer.RetryExhausted(numDelivered, b.cfg.Execution.Commands.MaxDeliver) {
			return msg.NakWithDelay(actorlayer.RetryDelay(numDelivered - 1))
		}
		reason := safeReason("event_projection_failed", actorlayer.ClassifyError(err))
		dlqEnv := decorateDLQEnvelope(env, reason, actorlayer.ClassifyError(err), b.cfg.Execution.Events.Stream, baldaexecution.DefaultEventProjectorConsumer, msg.Subject(), numDelivered)
		_ = b.publishDLQ(ctx, dlqEnv, reason, false)
		return msg.TermWithReason(reason)
	}
	return msg.DoubleAck(ctx)
}

func messageDeliveryAttempt(msg jetstream.Msg) int {
	if md, err := msg.Metadata(); err == nil && md.NumDelivered > 0 {
		return int(md.NumDelivered)
	}
	return 1
}

func ensureStreams(ctx context.Context, js jetstream.JetStream, cfg resolvedConfig) error {
	if js == nil {
		return fmt.Errorf("runtime transport is required")
	}
	streams := []jetstream.StreamConfig{
		streamConfig(cfg.Execution.Commands.Stream, []string{baldaexecution.SubjectCommandAll}, jetstream.WorkQueuePolicy, cfg.Commands),
		streamConfig(cfg.Execution.Events.Stream, []string{baldaexecution.SubjectEventAll}, jetstream.LimitsPolicy, cfg.Events),
		streamConfig(cfg.Execution.DLQ.Stream, []string{baldaexecution.SubjectDLQAll}, jetstream.LimitsPolicy, cfg.DLQ),
	}
	if cfg.Execution.Memory.Enabled {
		streams = append(streams, streamConfig(cfg.Execution.Memory.Stream, []string{sessionmemorycmd.SubjectAll}, jetstream.WorkQueuePolicy, cfg.Memory))
	}
	for _, stream := range streams {
		if _, err := js.CreateOrUpdateStream(ctx, stream); err != nil {
			return fmt.Errorf("create or update stream %s: %w", stream.Name, err)
		}
	}
	return nil
}

func streamConfig(name string, subjects []string, retention jetstream.RetentionPolicy, spec streamSpec) jetstream.StreamConfig {
	discard := jetstream.DiscardOld
	if spec.Discard == "new" {
		discard = jetstream.DiscardNew
	}
	return jetstream.StreamConfig{
		Name:       name,
		Subjects:   subjects,
		Retention:  retention,
		Storage:    jetstream.FileStorage,
		MaxAge:     spec.MaxAge,
		MaxBytes:   spec.MaxBytes,
		MaxMsgSize: spec.MaxMsgSize,
		Discard:    discard,
		Replicas:   1,
	}
}

func (b *Bus) publishRawDLQ(ctx context.Context, source jetstream.Msg, reason string) error {
	payload := payloadDiagnostics(source.Data(), "poison_message", string(actorlayer.ErrorKindDecode))
	payload["subject"] = source.Subject()
	payload["reason"] = sanitizeReason(reason)
	payload["header_count"] = len(source.Headers())
	if md, err := source.Metadata(); err == nil {
		payload["source_stream"] = strings.TrimSpace(md.Stream)
		payload["source_consumer"] = strings.TrimSpace(md.Consumer)
		payload["num_delivered"] = int(md.NumDelivered)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	env := actorlayer.Envelope{
		ID:        "poison-" + uuid.NewString(),
		Namespace: baldaexecution.NamespaceTelemetry,
		Kind:      "poison_message",
		From:      actorlayer.SystemAddress("transport"),
		To:        actorlayer.SystemAddress("dlq"),
		Payload: actorlayer.Payload{
			Encoding: actorlayer.EncodingJSON,
			Data:     data,
		},
	}
	msg, err := messageFromEnvelope(baldaexecution.SubjectDLQCommand, env)
	if err != nil {
		return err
	}
	msg.Header.Set("Balda-DLQ-Reason", sanitizeReason(reason))
	_, err = b.js.PublishMsg(ctx, msg, jetstream.WithExpectStream(b.cfg.Execution.DLQ.Stream), jetstream.WithMsgID(env.ID))
	if err != nil {
		return fmt.Errorf("publish raw dlq: %w", err)
	}
	return nil
}

func commandEventEnvelope(env actorlayer.Envelope, result *actortransport.DispatchReceipt, status string, reason string, extra map[string]any) actorlayer.Envelope {
	payload := map[string]any{
		"envelope_id":    env.ID,
		"job_id":         baldaexecution.EnvelopeJobID(env),
		"session_id":     baldaexecution.EnvelopeSessionID(env),
		"namespace":      env.Namespace,
		"status":         status,
		"correlation_id": env.CorrelationID,
		"causation_id":   env.CausationID,
		"actor_key":      strings.TrimSpace(env.To.Key),
	}
	if strings.EqualFold(strings.TrimSpace(env.To.Target), baldaexecution.ActorTypeDelivery) {
		payload["delivery_key"] = strings.TrimSpace(env.To.Key)
	}
	if result != nil {
		payload["stream"] = result.Stream
		payload["sequence"] = result.Sequence
		payload["subject"] = result.Subject
		payload["msg_id"] = result.MsgID
		payload["duplicate"] = result.Duplicate
	}
	if safeEventReason := commandEventReason(status, reason); safeEventReason != "" {
		payload["reason"] = safeEventReason
	}
	for key, value := range extra {
		if strings.TrimSpace(key) == "" {
			continue
		}
		payload[key] = value
	}
	data, _ := json.Marshal(payload)
	out := env
	out.ID = strings.TrimSpace(env.ID) + ":event:" + strings.TrimSpace(status)
	out.Namespace = baldaexecution.NamespaceTelemetry
	out.Kind = "command_event"
	out.Payload = actorlayer.Payload{
		Encoding: actorlayer.EncodingJSON,
		Data:     data,
	}
	out.DedupeKey = out.ID
	out.Meta = safeCommandMeta(env.Meta)
	if out.Meta == nil {
		out.Meta = make(map[string]string)
	}
	out.Meta["event_type"] = "command." + strings.TrimSpace(status)
	if out.From.Target == "" {
		out.From = actorlayer.SystemAddress("transport")
	}
	if out.To.Target == "" {
		out.To = actorlayer.SystemAddress("transport")
	}
	return out
}

func decorateDLQEnvelope(env actorlayer.Envelope, reason string, class actorlayer.ErrorKind, stream string, consumer string, subject string, numDelivered int) actorlayer.Envelope {
	out := env
	if out.Meta == nil {
		out.Meta = map[string]string{}
	}
	if class != "" {
		out.Meta[dlqMetaErrorClass] = string(class)
	}
	if trimmed := strings.TrimSpace(stream); trimmed != "" {
		out.Meta[dlqMetaSourceStream] = trimmed
	}
	if trimmed := strings.TrimSpace(consumer); trimmed != "" {
		out.Meta[dlqMetaSourceCns] = trimmed
	}
	if trimmed := strings.TrimSpace(subject); trimmed != "" {
		out.Meta[dlqMetaSourceSubj] = trimmed
	}
	if numDelivered > 0 {
		out.Meta[dlqMetaDelivered] = strconv.Itoa(numDelivered)
	}
	if trimmed := sanitizeReason(reason); trimmed != "" {
		out.Meta["reason"] = trimmed
	}
	return out
}

func diagnosticDLQEnvelope(env actorlayer.Envelope) actorlayer.Envelope {
	out := env
	out.DedupeKey = strings.TrimSpace(env.ID)
	out.Meta = safeDLQMeta(env.Meta)
	data, _ := json.Marshal(payloadDiagnostics(env.Payload.Data, env.Kind, strings.TrimSpace(out.Meta[dlqMetaErrorClass])))
	out.Payload = actorlayer.Payload{Encoding: actorlayer.EncodingJSON, Data: data}
	return out
}

func safeDLQMeta(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return nil
	}
	safe := make(map[string]string)
	for _, key := range []string{
		"job_id",
		"session_id",
		dlqMetaSourceStream,
		dlqMetaSourceCns,
		dlqMetaSourceSubj,
		dlqMetaDelivered,
	} {
		if value := strings.TrimSpace(meta[key]); value != "" {
			safe[key] = value
		}
	}
	if value := strings.TrimSpace(meta[dlqMetaErrorClass]); knownErrorClass(value) {
		safe[dlqMetaErrorClass] = value
	}
	if value := strings.TrimSpace(meta["reason"]); value != "" {
		safe["reason"] = sanitizeReason(value)
	}
	return safe
}

func safeCommandMeta(meta map[string]string) map[string]string {
	if len(meta) == 0 {
		return nil
	}
	safe := make(map[string]string)
	for _, key := range []string{"job_id", "session_id"} {
		if value := strings.TrimSpace(meta[key]); value != "" {
			safe[key] = value
		}
	}
	return safe
}

func payloadDiagnostics(data []byte, originalKind, errorClass string) map[string]any {
	sum := sha256.Sum256(data)
	payload := map[string]any{
		"original_kind":  strings.TrimSpace(originalKind),
		"payload_bytes":  len(data),
		"payload_sha256": hex.EncodeToString(sum[:]),
	}
	if trimmed := strings.TrimSpace(errorClass); trimmed != "" {
		payload["error_class"] = trimmed
	}
	return payload
}

func safeReason(stage string, class actorlayer.ErrorKind) string {
	stage = strings.TrimSpace(stage)
	if stage == "" {
		stage = settlementStageTerminal
	}
	className := strings.TrimSpace(string(class))
	if className == "" {
		className = "unclassified"
	}
	return stage + ":" + className
}

func sanitizeReason(reason string) string {
	reason = strings.TrimSpace(reason)
	parts := strings.Split(reason, ":")
	if len(parts) == 2 && knownSettlementStage(parts[0]) && knownErrorClass(parts[1]) {
		return parts[0] + ":" + parts[1]
	}
	if knownSettlementStage(reason) {
		return reason
	}
	return settlementTerminalUnclassified
}

func sanitizeReasonForStage(reason, fallbackStage string) string {
	sanitized := sanitizeReason(reason)
	if sanitized != settlementTerminalUnclassified || strings.TrimSpace(fallbackStage) == settlementStageTerminal {
		return sanitized
	}
	return safeReason(fallbackStage, "")
}

func commandEventReason(status, reason string) string {
	if strings.TrimSpace(reason) == "" {
		return ""
	}
	if strings.TrimSpace(status) == "noop" {
		return "duplicate publish suppressed"
	}
	return sanitizeReason(reason)
}

func knownSettlementStage(value string) bool {
	switch value {
	case "decode_failed", "event_projection_failed", "retry_exhausted", "retrying", settlementStageTerminal:
		return true
	default:
		return false
	}
}

func knownErrorClass(value string) bool {
	switch actorlayer.ErrorKind(value) {
	case actorlayer.ErrorKindTransient,
		actorlayer.ErrorKindPolicy,
		actorlayer.ErrorKindPermanent,
		actorlayer.ErrorKindDecode,
		actorlayer.ErrorKindExternalDelivery:
		return true
	default:
		return value == "unclassified"
	}
}

func commandLogEvent(evt *zerolog.Event, msg jetstream.Msg) *zerolog.Event {
	evt = evt.
		Str("subject", msg.Subject()).
		Int("delivery_attempt", messageDeliveryAttempt(msg))
	if md, err := msg.Metadata(); err == nil {
		evt = evt.
			Str("stream", md.Stream).
			Uint64("stream_sequence", md.Sequence.Stream).
			Uint64("consumer_sequence", md.Sequence.Consumer)
	}
	return evt
}

func commandLogEnvelope(evt *zerolog.Event, env actorlayer.Envelope) *zerolog.Event {
	to, _ := env.To.String()
	evt = evt.
		Str("envelope_id", strings.TrimSpace(env.ID)).
		Str("namespace", strings.TrimSpace(env.Namespace)).
		Str("task_id", baldaexecution.EnvelopeJobID(env)).
		Str("session_id", baldaexecution.EnvelopeSessionID(env)).
		Str("correlation_id", strings.TrimSpace(env.CorrelationID)).
		Str("causation_id", strings.TrimSpace(env.CausationID)).
		Str("actor_key", strings.TrimSpace(env.To.Key)).
		Str("to", strings.TrimSpace(to))
	return withDeliveryKey(evt, env)
}
