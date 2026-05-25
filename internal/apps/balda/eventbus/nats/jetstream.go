package natsbus

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	gnats "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/normahq/balda/internal/apps/balda/swarm"
)

type commandMessage struct {
	subject string
	env     swarm.Envelope
	msg     jetstream.Msg
}

func (m commandMessage) Envelope() swarm.Envelope { return m.env }
func (m commandMessage) Subject() string          { return m.subject }
func (m commandMessage) InProgress(context.Context) error {
	return m.msg.InProgress()
}

func (b *Bus) RunCommandConsumer(ctx context.Context, handler swarm.CommandHandler) error {
	if b == nil || b.consumer == nil {
		return fmt.Errorf("jetstream command consumer is required")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		batch, err := b.consumer.Fetch(b.cfg.Swarm.Commands.FetchBatch, jetstream.FetchMaxWait(b.cfg.FetchWait))
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			continue
		}
		for msg := range batch.Messages() {
			if err := b.handleMessage(ctx, msg, handler); err != nil {
				b.logger.Warn().Err(err).Str("subject", msg.Subject()).Msg("failed to settle jetstream command")
			}
		}
	}
}

func (b *Bus) handleMessage(ctx context.Context, msg jetstream.Msg, handler swarm.CommandHandler) error {
	env, err := swarm.DecodeEnvelope(string(msg.Data()))
	if err != nil {
		_ = msg.TermWithReason("decode failed: " + err.Error())
		return err
	}
	if md, metaErr := msg.Metadata(); metaErr == nil && md.NumDelivered > 0 {
		env.Attempt = int(md.NumDelivered) - 1
	}
	cmd := commandMessage{subject: msg.Subject(), env: env, msg: msg}
	_ = b.PublishEvent(ctx, swarm.SubjectEventCommandRunning, commandEventEnvelope(env, nil, "running", ""))
	err = handler(ctx, cmd)
	if err == nil {
		_ = b.PublishEvent(ctx, swarm.SubjectEventCommandAcked, commandEventEnvelope(env, nil, "acked", ""))
		return msg.DoubleAck(ctx)
	}
	if isRetryable(err) {
		delay := computeBackoff(env.Attempt)
		_ = b.PublishEvent(ctx, swarm.SubjectEventCommandRetrying, commandEventEnvelope(env, nil, "retrying", err.Error()))
		return msg.NakWithDelay(delay)
	}
	_ = b.PublishDLQ(ctx, env, err.Error())
	return msg.TermWithReason(err.Error())
}

func ensureStreams(ctx context.Context, js jetstream.JetStream, cfg resolvedConfig) error {
	if js == nil {
		return fmt.Errorf("jetstream is required")
	}
	streams := []jetstream.StreamConfig{
		streamConfig(cfg.Swarm.Commands.Stream, []string{swarm.SubjectCommandAll}, jetstream.WorkQueuePolicy, cfg.Commands),
		streamConfig(cfg.Swarm.Events.Stream, []string{swarm.SubjectEventAll}, jetstream.LimitsPolicy, cfg.Events),
		streamConfig(cfg.Swarm.DLQ.Stream, []string{swarm.SubjectDLQAll}, jetstream.LimitsPolicy, cfg.DLQ),
	}
	for _, stream := range streams {
		if _, err := js.CreateOrUpdateStream(ctx, stream); err != nil {
			return fmt.Errorf("create or update stream %s: %w", stream.Name, err)
		}
	}
	return nil
}

func streamConfig(name string, subjects []string, retention jetstream.RetentionPolicy, spec streamSpec) jetstream.StreamConfig {
	return jetstream.StreamConfig{
		Name:       name,
		Subjects:   subjects,
		Retention:  retention,
		Storage:    jetstream.FileStorage,
		MaxAge:     spec.MaxAge,
		MaxBytes:   spec.MaxBytes,
		MaxMsgSize: spec.MaxMsgSize,
		Discard:    discardPolicy(spec.Discard),
		Replicas:   1,
	}
}

func discardPolicy(raw string) jetstream.DiscardPolicy {
	if raw == "new" {
		return jetstream.DiscardNew
	}
	return jetstream.DiscardOld
}

func newDLQMessage(env swarm.Envelope, reason string) (*gnats.Msg, error) {
	msg, err := messageFromEnvelope(swarm.SubjectDLQCommand, env)
	if err != nil {
		return nil, err
	}
	msg.Header.Set("Balda-DLQ-Reason", reason)
	return msg, nil
}

func isRetryable(err error) bool {
	switch swarm.ClassifyError(err) {
	case swarm.ErrorKindDuplicate, swarm.ErrorKindAuth, swarm.ErrorKindPolicy, swarm.ErrorKindPermanent:
		return false
	default:
		return true
	}
}

func computeBackoff(attempt int) time.Duration {
	if attempt < 0 {
		attempt = 0
	}
	delay := time.Second
	for range attempt {
		delay *= 2
		if delay >= time.Minute {
			return time.Minute
		}
	}
	return delay
}

func commandEventEnvelope(env swarm.Envelope, result *swarm.CommandPublishResult, status string, reason string) swarm.Envelope {
	payload := map[string]any{
		"envelope_id": env.ID,
		"task_id":     env.TaskID,
		"session_id":  env.SessionID,
		"namespace":   env.Namespace,
		"status":      status,
	}
	if result != nil {
		payload["stream"] = result.Stream
		payload["sequence"] = result.Sequence
		payload["subject"] = result.Subject
		payload["msg_id"] = result.MsgID
		payload["duplicate"] = result.Duplicate
	}
	if strings.TrimSpace(reason) != "" {
		payload["reason"] = reason
	}
	data, _ := json.Marshal(payload)
	out := env
	out.ID = strings.TrimSpace(env.ID) + ":event:" + strings.TrimSpace(status)
	out.Namespace = swarm.NamespaceTelemetry
	out.Kind = "command_event"
	out.PayloadJSON = string(data)
	if out.From.Target == "" {
		out.From = swarm.SystemAddress("jetstream")
	}
	if out.To.Target == "" {
		out.To = swarm.SystemAddress("jetstream")
	}
	return out
}
