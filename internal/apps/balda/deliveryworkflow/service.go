package deliveryworkflow

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/google/uuid"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

type Lifecycle interface {
	ReserveDelivery(ctx context.Context, record baldastate.DeliveryRecord) (baldastate.DeliveryRecord, bool, error)
	MarkDeliverySending(ctx context.Context, deliveryKey string) error
	MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error
	MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error
	AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error
}

type QuestionDeliveryBinder interface {
	BindDelivery(ctx context.Context, questionID string, ref questioncmd.DeliveryRef) error
	DeliveryState(ctx context.Context, questionID string) (string, bool, error)
	FailedDeliveryContinuation(ctx context.Context, questionID string) (actorlayer.Envelope, bool, error)
	FailDelivery(ctx context.Context, questionID string, failure questioncmd.Failure) (actorlayer.Envelope, bool, error)
}

type Service struct {
	dispatcher Dispatcher
	registry   *deliveryfmt.Registry
	outbox     DeliveryStore
	events     JobEvents
	questions  QuestionDeliveryBinder
	actor      actortransport.Dispatcher
	logger     zerolog.Logger
}

func New(dispatcher Dispatcher, outbox DeliveryStore, events JobEvents, questions QuestionDeliveryBinder, actor actortransport.Dispatcher, logger zerolog.Logger) *Service {
	return NewWithRegistry(dispatcher, nil, outbox, events, questions, actor, logger)
}

func NewWithRegistry(dispatcher Dispatcher, registry *deliveryfmt.Registry, outbox DeliveryStore, events JobEvents, questions QuestionDeliveryBinder, actor actortransport.Dispatcher, logger zerolog.Logger) *Service {
	return &Service{dispatcher: dispatcher, registry: registry, outbox: outbox, events: events, questions: questions, actor: actor, logger: logger}
}

func (s *Service) Handle(ctx context.Context, env actorlayer.Envelope, payload deliverycmd.Payload) error {
	if s.dispatcher == nil {
		return actorlayer.TransientError(fmt.Errorf("delivery dispatcher is required"))
	}
	if err := deliverycmd.Validate(payload); err != nil {
		return actorlayer.PermanentError(err)
	}
	if handled, err := s.resumeSettledQuestionDelivery(ctx, payload); err != nil {
		return actorlayer.TransientError(err)
	} else if handled {
		return nil
	}
	envelopeJobID := strings.TrimSpace(baldaexecution.EnvelopeJobID(env))
	payloadJobID := strings.TrimSpace(payload.JobID)
	switch {
	case envelopeJobID == "" && payloadJobID == "":
	case envelopeJobID == "":
		return actorlayer.PolicyError(fmt.Errorf("delivery envelope job scope is required when payload job id is set"))
	case payloadJobID == "":
		return actorlayer.PolicyError(fmt.Errorf("delivery payload job id is required when envelope job scope is set"))
	case envelopeJobID != payloadJobID:
		return actorlayer.PolicyError(fmt.Errorf("delivery job scope mismatch: envelope=%q payload=%q", envelopeJobID, payloadJobID))
	}
	delivery, err := s.prepareDelivery(payload)
	if err != nil {
		s.logSettlement(payload, env.ID, env.Attempt+1, preparationFailureStage(err), "permanent")
		return actorlayer.PermanentError(err)
	}
	durable := RequiresOutbox(payload)
	deliveryKey := strings.TrimSpace(env.DedupeKey)
	if deliveryKey == "" {
		deliveryKey = strings.TrimSpace(env.ID)
	}
	if deliveryKey == "" {
		deliveryKey = "delivery:" + shortJobHash(env.Payload.String())
	}
	sum := sha256.Sum256(env.Payload.Data)
	payloadHash := hex.EncodeToString(sum[:])
	if durable && s.outbox != nil {
		record, created, err := s.outbox.ReserveDelivery(ctx, baldastate.DeliveryRecord{
			ID:          uuid.NewString(),
			DeliveryKey: deliveryKey,
			JobID:       payload.JobID,
			SessionID:   payload.Locator.SessionID,
			Channel:     firstNonEmpty(payload.Locator.ChannelType, "telegram"),
			AddressKey:  firstNonEmpty(payload.Locator.AddressKey, payload.Locator.SessionID),
			Kind:        env.Kind,
			Payload:     strings.TrimSpace(env.Payload.String()),
			PayloadHash: payloadHash,
			Status:      baldastate.DeliveryStatusPending,
		})
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if record.PayloadHash != "" && record.PayloadHash != payloadHash {
			return actorlayer.PermanentError(fmt.Errorf("delivery key %q already reserved for different payload", deliveryKey))
		}
		if record.Status == baldastate.DeliveryStatusSent {
			if err := s.bindQuestionDelivery(ctx, payload, record.ProviderMessageID); err != nil {
				return actorlayer.TransientError(err)
			}
			return nil
		}
		if !created && !ReadyForAttempt(record) {
			if record.Status == baldastate.DeliveryStatusSending {
				return actorlayer.TransientError(fmt.Errorf("delivery %q has ambiguous sending status; automatic resend is disabled; last updated at %s", deliveryKey, record.UpdatedAt.Format(time.RFC3339)))
			}
			return actorlayer.TransientError(fmt.Errorf("delivery %q is already %s; last updated at %s", deliveryKey, record.Status, record.UpdatedAt.Format(time.RFC3339)))
		}
		if err := s.outbox.MarkDeliverySending(ctx, deliveryKey); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	if payload.Progress != nil && payload.Progress.Kind == deliverycmd.ProgressThinking {
		s.logger.Debug().
			Str("session_id", payload.Locator.SessionID).
			Bool("visible", payload.Progress.Visible).
			Bool("policy_thinking", payload.Progress.Policy.Thinking).
			Int("text_char_count", len(strings.TrimSpace(payload.Progress.Text))).
			Int("sequence", payload.Progress.Sequence).
			Msg("dispatching thinking progress delivery")
	}
	providerMessageID, err := s.dispatcher.Dispatch(ctx, delivery)
	if err != nil {
		deliveryErrorKind, classified := deliverycmd.ClassifyError(err)
		retryable := classified && deliveryErrorKind == deliverycmd.ErrorKindRetryable
		settlementClass := deliveryFailureClass(err)
		s.logSettlement(payload, env.ID, env.Attempt+1, "resolved", settlementClass)
		if durable && s.outbox != nil {
			_ = s.outbox.MarkDeliveryFailed(ctx, deliveryKey, settlementClass)
			if !retryable && strings.TrimSpace(payload.JobID) != "" {
				if s.events != nil {
					if appendErr := s.events.AppendEvent(ctx, payload.JobID, baldajobs.JobEventDeliveryFailed, "delivery.actor", env.ID, map[string]any{
						"mode":            payload.Mode,
						"text_char_count": len(strings.TrimSpace(payload.Text)),
						"has_action":      strings.TrimSpace(payload.Action) != "",
						"error_class":     settlementClass,
					}); appendErr != nil {
						s.logger.Warn().
							Str("job_id", payload.JobID).
							Str("error_class", "event_write_failed").
							Msg("failed to record job delivery failure event")
					}
				}
			}
		}
		if retryable {
			return actorlayer.ExternalDeliveryError(err)
		}
		if handled, failErr := s.failQuestionDelivery(ctx, payload, err); failErr != nil {
			return actorlayer.TransientError(failErr)
		} else if handled {
			return nil
		}
		if classified && deliveryErrorKind == deliverycmd.ErrorKindPermanent {
			return actorlayer.PermanentError(err)
		}
		return actorlayer.ExternalDeliveryError(err)
	}
	if durable && s.outbox != nil {
		if err := s.outbox.MarkDeliverySent(ctx, deliveryKey, providerMessageID); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	if err := s.bindQuestionDelivery(ctx, payload, providerMessageID); err != nil {
		return actorlayer.TransientError(err)
	}
	if durable && s.events != nil && strings.TrimSpace(payload.JobID) != "" {
		if err := s.events.AppendEvent(ctx, payload.JobID, baldajobs.JobEventDeliverySent, "delivery.actor", env.ID, map[string]any{
			"mode":                payload.Mode,
			"provider":            strings.TrimSpace(payload.Locator.ChannelType),
			"conversation_key":    strings.TrimSpace(payload.Locator.AddressKey),
			"provider_message_id": strings.TrimSpace(providerMessageID),
			"text_char_count":     len(strings.TrimSpace(payload.Text)),
			"refs_count":          len(payload.Refs),
		}); err != nil {
			s.logger.Warn().
				Str("job_id", payload.JobID).
				Str("error_class", "event_write_failed").
				Msg("failed to record job delivery event")
		}
	}
	s.logSettlement(payload, env.ID, env.Attempt+1, "resolved", "success")
	return nil
}

type preparationError struct {
	stage string
	err   error
}

func (e *preparationError) Error() string { return e.err.Error() }
func (e *preparationError) Unwrap() error { return e.err }

func (s *Service) prepareDelivery(payload deliverycmd.Payload) (Delivery, error) {
	delivery := Delivery{Payload: payload}
	text, formatted := formattedText(payload)
	if !formatted {
		return delivery, nil
	}
	if s.registry == nil {
		return Delivery{}, &preparationError{stage: "registry_unavailable", err: fmt.Errorf("message format registry is required")}
	}
	name, _, formatter, err := s.registry.Resolve(payload.Locator.ChannelType, payload.DeliveryFormat)
	if err != nil {
		return Delivery{}, &preparationError{stage: "route_resolution_failed", err: fmt.Errorf("resolve delivery message format: %w", err)}
	}
	message, err := formatter.Format(text)
	if err != nil {
		return Delivery{}, &preparationError{stage: "formatter_failed", err: fmt.Errorf("format delivery message %q: %w", name, err)}
	}
	if message.Name != name {
		return Delivery{}, &preparationError{stage: "formatter_contract_failed", err: fmt.Errorf("format delivery message %q: formatter returned name %q", name, message.Name)}
	}
	delivery.Message = &message
	return delivery, nil
}

func preparationFailureStage(err error) string {
	var preparationErr *preparationError
	if errors.As(err, &preparationErr) && strings.TrimSpace(preparationErr.stage) != "" {
		return preparationErr.stage
	}
	return "preparation_failed"
}

func deliveryFailureClass(err error) string {
	kind, classified := deliverycmd.ClassifyError(err)
	if !classified {
		return "ambiguous"
	}
	return string(kind)
}

func (s *Service) logSettlement(payload deliverycmd.Payload, envelopeID string, attempt int, resolutionOutcome, settlementClass string) {
	event := s.logger.Debug()
	if settlementClass != "success" {
		event = s.logger.Warn()
	}
	event.
		Str("transport", strings.TrimSpace(payload.Locator.ChannelType)).
		Str("session_id", strings.TrimSpace(payload.Locator.SessionID)).
		Str("delivery_format", string(deliveryfmt.NormalizeDeliveryFormat(payload.DeliveryFormat))).
		Str("operation", string(payload.Mode)).
		Str("envelope_id", strings.TrimSpace(envelopeID)).
		Int("attempt", attempt).
		Str("resolution_outcome", strings.TrimSpace(resolutionOutcome)).
		Str("settlement_class", strings.TrimSpace(settlementClass)).
		Msg("delivery settled")
}

func formattedText(payload deliverycmd.Payload) (string, bool) {
	if deliveryfmt.NormalizeDeliveryFormat(payload.DeliveryFormat) == "" {
		return "", false
	}
	switch payload.Mode {
	case deliverycmd.ModeAgentReply, deliverycmd.ModeMarkdown:
		return payload.Text, true
	case deliverycmd.ModeProgress:
		if payload.Progress != nil && payload.Progress.Visible && strings.TrimSpace(payload.Progress.Text) != "" {
			return payload.Progress.Text, true
		}
	case deliverycmd.ModePhoto, deliverycmd.ModeDocument:
		if payload.Media != nil && strings.TrimSpace(payload.Media.Caption) != "" {
			return payload.Media.Caption, true
		}
	}
	return "", false
}

func (s *Service) resumeSettledQuestionDelivery(ctx context.Context, payload deliverycmd.Payload) (bool, error) {
	questionID := questionIDFromPayload(payload)
	if s.questions == nil || questionID == "" {
		return false, nil
	}
	status, found, err := s.questions.DeliveryState(ctx, questionID)
	if err != nil || !found || status == questioncmd.StatusPending {
		return false, err
	}
	if status != questioncmd.StatusFailed {
		return true, nil
	}
	envelope, failed, err := s.questions.FailedDeliveryContinuation(ctx, questionID)
	if err != nil || !failed {
		return false, err
	}
	return true, s.dispatchQuestionContinuation(ctx, envelope)
}

func (s *Service) failQuestionDelivery(ctx context.Context, payload deliverycmd.Payload, deliveryErr error) (bool, error) {
	questionID := questionIDFromPayload(payload)
	if s.questions == nil || questionID == "" {
		return false, nil
	}
	envelope, failed, err := s.questions.FailDelivery(ctx, questionID, questioncmd.Failure{
		Code:     "delivery_failed",
		Message:  "delivery failed: " + deliveryFailureClass(deliveryErr),
		FailedAt: time.Now().UTC(),
	})
	if err != nil || !failed {
		return false, err
	}
	return true, s.dispatchQuestionContinuation(ctx, envelope)
}

func (s *Service) dispatchQuestionContinuation(ctx context.Context, envelope actorlayer.Envelope) error {
	if s.actor == nil {
		return fmt.Errorf("actor dispatcher is required for question delivery failure")
	}
	_, err := s.actor.Dispatch(ctx, envelope)
	if err != nil {
		return fmt.Errorf("dispatch question delivery failure: %w", err)
	}
	return nil
}

func questionIDFromPayload(payload deliverycmd.Payload) string {
	if payload.Refs == nil {
		return ""
	}
	return strings.TrimSpace(payload.Refs["question_id"])
}

func (s *Service) bindQuestionDelivery(ctx context.Context, payload deliverycmd.Payload, providerMessageID string) error {
	if s.questions == nil || payload.Refs == nil {
		return nil
	}
	questionID := strings.TrimSpace(payload.Refs["question_id"])
	providerMessageID = strings.TrimSpace(providerMessageID)
	if questionID == "" || providerMessageID == "" {
		return nil
	}
	return s.questions.BindDelivery(ctx, questionID, questioncmd.DeliveryRef{
		Provider:          strings.TrimSpace(payload.Locator.ChannelType),
		ConversationKey:   strings.TrimSpace(payload.Locator.AddressKey),
		ProviderMessageID: providerMessageID,
		ControlHandle:     strings.TrimSpace(payload.Refs["question_control_handle"]),
	})
}

func RequiresOutbox(payload deliverycmd.Payload) bool {
	switch payload.Mode {
	case deliverycmd.ModeAgentReply, deliverycmd.ModePlain, deliverycmd.ModeMarkdown:
	default:
		return false
	}
	switch payload.Settlement {
	case deliverycmd.SettlementBypass:
		return false
	case deliverycmd.SettlementOutbox:
		return true
	case "", deliverycmd.SettlementAuto:
		return strings.TrimSpace(payload.JobID) != ""
	default:
		return strings.TrimSpace(payload.JobID) != ""
	}
}

func ReadyForAttempt(record baldastate.DeliveryRecord) bool {
	switch record.Status {
	case baldastate.DeliveryStatusSent:
		return false
	case baldastate.DeliveryStatusSending:
		return false
	case baldastate.DeliveryStatusFailed:
		return true
	case baldastate.DeliveryStatusPending:
		if record.UpdatedAt.IsZero() {
			return true
		}
		return time.Since(record.UpdatedAt) >= 30*time.Second
	default:
		return true
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func shortJobHash(input string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(input)))
	return hex.EncodeToString(sum[:])[:12]
}
