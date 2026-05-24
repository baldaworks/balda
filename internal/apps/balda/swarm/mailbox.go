package swarm

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

const (
	DefaultClaimLimit    = 8
	DefaultLeaseDuration = 30 * time.Second
)

type MailboxService struct {
	store baldastate.SwarmStore
	bus   WakeBus
	cfg   Config
}

type mailboxServiceParams struct {
	fx.In

	StateProvider baldastate.Provider
	Bus           WakeBus
	Config        Config
}

func NewMailboxService(params mailboxServiceParams) (*MailboxService, error) {
	if params.StateProvider == nil {
		return nil, fmt.Errorf("balda state provider is required")
	}
	if params.Bus == nil {
		return nil, fmt.Errorf("swarm wake bus is required")
	}
	return &MailboxService{store: params.StateProvider.Swarm(), bus: params.Bus, cfg: params.Config}, nil
}

func (s *MailboxService) Enabled() bool {
	return s != nil && s.cfg.MailboxEnabled()
}

type SubmittedMessage struct {
	MessageID     string
	MailboxID     string
	QueuePosition int
	Published     bool
}

func (s *MailboxService) Publish(ctx context.Context, env Envelope) (SubmittedMessage, error) {
	if !s.Enabled() {
		return SubmittedMessage{}, fmt.Errorf("swarm mailbox runtime is disabled")
	}
	if strings.TrimSpace(env.ID) == "" {
		env.ID = uuid.NewString()
	}
	if err := env.Validate(); err != nil {
		return SubmittedMessage{}, err
	}
	mailbox, err := env.To.MailboxID()
	if err != nil {
		return SubmittedMessage{}, err
	}
	record, err := envelopeToRecord(env, mailbox)
	if err != nil {
		return SubmittedMessage{}, err
	}
	result, err := s.store.Publish(ctx, record)
	if err != nil {
		return SubmittedMessage{}, err
	}
	if result.Published {
		if err := s.Notify(ctx, env.To); err != nil {
			return SubmittedMessage{}, err
		}
	}
	return SubmittedMessage{MessageID: env.ID, MailboxID: mailbox, Published: result.Published}, nil
}

func (s *MailboxService) PublishBatch(ctx context.Context, envs []Envelope) error {
	if !s.Enabled() {
		return fmt.Errorf("swarm mailbox runtime is disabled")
	}
	if len(envs) == 0 {
		return nil
	}
	records := make([]baldastate.SwarmMessageRecord, 0, len(envs))
	mailboxes := make(map[string]ActorAddress, len(envs))
	for idx := range envs {
		if strings.TrimSpace(envs[idx].ID) == "" {
			envs[idx].ID = uuid.NewString()
		}
		if err := envs[idx].Validate(); err != nil {
			return err
		}
		mailbox, err := envs[idx].To.MailboxID()
		if err != nil {
			return err
		}
		record, err := envelopeToRecord(envs[idx], mailbox)
		if err != nil {
			return err
		}
		records = append(records, record)
		mailboxes[mailbox] = envs[idx].To
	}
	results, err := s.store.PublishBatch(ctx, records)
	if err != nil {
		return err
	}
	for _, result := range results {
		if !result.Published {
			continue
		}
		addr, ok := mailboxes[result.Mailbox]
		if !ok {
			continue
		}
		if err := s.Notify(ctx, addr); err != nil {
			return err
		}
	}
	return nil
}

func (s *MailboxService) Claim(ctx context.Context, mailbox string, owner string, limit int, lease time.Duration) ([]Envelope, error) {
	if !s.Enabled() {
		return nil, nil
	}
	records, err := s.store.Claim(ctx, mailbox, owner, limit, lease)
	if err != nil {
		return nil, err
	}
	envs := make([]Envelope, 0, len(records))
	for _, record := range records {
		env, err := recordToEnvelope(record)
		if err != nil {
			return nil, err
		}
		envs = append(envs, env)
	}
	return envs, nil
}

func (s *MailboxService) Ack(ctx context.Context, mailbox string, messageID string) error {
	return s.store.Ack(ctx, mailbox, messageID)
}

func (s *MailboxService) Retry(ctx context.Context, mailbox string, messageID string, next time.Time, reason string) error {
	return s.store.Retry(ctx, mailbox, messageID, next, reason)
}

func (s *MailboxService) DeadLetter(ctx context.Context, mailbox string, messageID string, reason string) error {
	return s.store.DeadLetter(ctx, mailbox, messageID, reason)
}

func (s *MailboxService) CancelByTask(ctx context.Context, taskID string, reason string) (int, error) {
	return s.store.CancelByTask(ctx, taskID, reason)
}

func (s *MailboxService) CancelBySession(ctx context.Context, sessionID string, reason string) (int, error) {
	return s.store.CancelBySession(ctx, sessionID, reason)
}

func (s *MailboxService) Recover(ctx context.Context, now time.Time) (baldastate.SwarmRecoveryResult, error) {
	return s.store.Recover(ctx, now)
}

func (s *MailboxService) ListReadyMailboxes(ctx context.Context, limit int) ([]string, error) {
	return s.store.ListReadyMailboxes(ctx, limit)
}

func (s *MailboxService) Notify(ctx context.Context, addr ActorAddress) error {
	if !s.Enabled() {
		return nil
	}
	return s.bus.Publish(ctx, addr)
}

func envelopeToRecord(env Envelope, mailbox string) (baldastate.SwarmMessageRecord, error) {
	from, err := env.From.String()
	if err != nil {
		return baldastate.SwarmMessageRecord{}, err
	}
	to, err := env.To.String()
	if err != nil {
		return baldastate.SwarmMessageRecord{}, err
	}
	metaJSON := ""
	if len(env.Meta) > 0 {
		data, err := json.Marshal(env.Meta)
		if err != nil {
			return baldastate.SwarmMessageRecord{}, fmt.Errorf("encode envelope meta: %w", err)
		}
		metaJSON = string(data)
	}
	return baldastate.SwarmMessageRecord{
		ID:            strings.TrimSpace(env.ID),
		Mailbox:       mailbox,
		Namespace:     strings.TrimSpace(env.Namespace),
		Kind:          strings.TrimSpace(env.Kind),
		FromAddr:      from,
		ToAddr:        to,
		SessionID:     strings.TrimSpace(env.SessionID),
		TaskID:        strings.TrimSpace(env.TaskID),
		CorrelationID: strings.TrimSpace(env.CorrelationID),
		CausationID:   strings.TrimSpace(env.CausationID),
		Priority:      env.Priority,
		DedupeKey:     strings.TrimSpace(env.DedupeKey),
		MaxAttempts:   env.MaxAttempts,
		NotBefore:     env.NotBefore,
		ExpiresAt:     env.ExpiresAt,
		PayloadJSON:   strings.TrimSpace(env.PayloadJSON),
		MetaJSON:      metaJSON,
	}, nil
}

func recordToEnvelope(record baldastate.SwarmMessageRecord) (Envelope, error) {
	from, err := parseActorAddress(record.FromAddr)
	if err != nil {
		return Envelope{}, fmt.Errorf("parse from address: %w", err)
	}
	to, err := parseActorAddress(record.ToAddr)
	if err != nil {
		return Envelope{}, fmt.Errorf("parse to address: %w", err)
	}
	meta := map[string]string(nil)
	if strings.TrimSpace(record.MetaJSON) != "" {
		if err := json.Unmarshal([]byte(record.MetaJSON), &meta); err != nil {
			return Envelope{}, fmt.Errorf("decode envelope meta: %w", err)
		}
	}
	return Envelope{
		ID:            record.ID,
		Namespace:     record.Namespace,
		Kind:          record.Kind,
		From:          from,
		To:            to,
		SessionID:     record.SessionID,
		TaskID:        record.TaskID,
		CorrelationID: record.CorrelationID,
		CausationID:   record.CausationID,
		Priority:      record.Priority,
		DedupeKey:     record.DedupeKey,
		Attempt:       record.Attempt,
		MaxAttempts:   record.MaxAttempts,
		NotBefore:     record.NotBefore,
		ExpiresAt:     record.ExpiresAt,
		PayloadJSON:   record.PayloadJSON,
		Meta:          meta,
	}, nil
}

func parseActorAddress(raw string) (ActorAddress, error) {
	trimmed := strings.TrimSpace(raw)
	idx := strings.Index(trimmed, ":")
	if idx <= 0 || idx == len(trimmed)-1 {
		return ActorAddress{}, fmt.Errorf("invalid actor address %q", raw)
	}
	return ActorAddress{Target: trimmed[:idx], Key: trimmed[idx+1:]}, nil
}
