package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog/log"
	"go.uber.org/fx"
)

const (
	JobEventCreated        = "job.created"
	JobEventAssigned       = "job.assigned"
	JobEventStarted        = "job.started"
	JobEventAgentStarted   = "agent.started"
	JobEventAgentProgress  = "agent.progress"
	JobEventAgentResult    = "agent.result"
	JobEventValidating     = "job.validating"
	JobEventCompleted      = "job.completed"
	JobEventFailed         = "job.failed"
	JobEventCanceled       = "job.canceled"
	JobEventDeliverySent   = "delivery.sent"
	JobEventDeliveryFailed = "delivery.failed"
)

type JobService struct {
	store ServiceStore
	bus   actortransport.EventPublisher
}

// ServiceStore is the job state needed by JobService.
type ServiceStore interface {
	baldastate.JobLifecycleStore
	baldastate.JobEventOutboxStore
	baldastate.DeliveryStore
	baldastate.AgentStepStore
}

type jobServiceParams struct {
	fx.In

	Store ServiceStore
	Bus   actortransport.EventPublisher `optional:"true"`
}

func NewJobService(params jobServiceParams) (*JobService, error) {
	if params.Store == nil {
		return nil, fmt.Errorf("job service store is required")
	}
	return &JobService{store: params.Store, bus: params.Bus}, nil
}

func (s *JobService) Create(ctx context.Context, record baldastate.JobRecord, actor string, payload any) (bool, error) {
	if s == nil {
		return false, nil
	}
	payloadJSON, err := marshalPayload(payload)
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(payloadJSON) == "" {
		payloadJSON = "{}"
	}
	jobID := strings.TrimSpace(record.ID)
	event := baldastate.JobEventRecord{
		ID:          "job:" + jobID + ":event:created",
		JobID:       jobID,
		EventType:   JobEventCreated,
		Actor:       strings.TrimSpace(actor),
		PayloadJSON: payloadJSON,
	}
	outbox, err := jobEventOutboxRecord(event)
	if err != nil {
		return false, err
	}
	created, err := s.store.CreateJobWithEvent(ctx, record, outbox)
	if err != nil {
		return false, err
	}
	s.publishOutboxBestEffort(ctx, outbox)
	return created, nil
}

func (s *JobService) Get(ctx context.Context, jobID string) (baldastate.JobRecord, bool, error) {
	if s == nil {
		return baldastate.JobRecord{}, false, nil
	}
	return s.store.GetJob(ctx, jobID)
}

func (s *JobService) ListActiveJobsBySession(ctx context.Context, sessionID string) ([]baldastate.JobRecord, error) {
	if s == nil {
		return nil, nil
	}
	return s.store.ListActiveJobsBySession(ctx, sessionID)
}

func (s *JobService) ListActiveGoalJobsBySession(ctx context.Context, sessionID string) ([]baldastate.JobRecord, error) {
	if s == nil {
		return nil, nil
	}
	jobs, err := s.store.ListActiveJobsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	out := make([]baldastate.JobRecord, 0, len(jobs))
	for _, job := range jobs {
		if IsGoalJob(job) {
			out = append(out, job)
		}
	}
	return out, nil
}

func (s *JobService) MarkStatus(ctx context.Context, jobID string, status string, actor string, messageID string, reason string, payload any) error {
	if s == nil {
		return nil
	}
	eventType := jobStatusEventType(status)
	if eventType == "" {
		return fmt.Errorf("job status %q has no lifecycle event", status)
	}
	event, err := jobEventRecord(jobID, eventType, actor, messageID, mergePayload(payload, map[string]any{
		"status": status,
		"reason": reason,
	}))
	if err != nil {
		return err
	}
	outbox, err := jobEventOutboxRecord(event)
	if err != nil {
		return err
	}
	if err := s.store.UpdateJobStatusWithEvent(ctx, jobID, status, reason, outbox); err != nil {
		return s.suppressStaleTerminalTransition(ctx, jobID, status, err)
	}
	s.publishOutboxBestEffort(ctx, outbox)
	return nil
}

func (s *JobService) SetResult(ctx context.Context, jobID string, result any, status string, actor string, reason string) error {
	if s == nil {
		return nil
	}
	data, err := marshalPayload(result)
	if err != nil {
		return err
	}
	eventType := jobStatusEventType(status)
	if eventType == "" {
		return fmt.Errorf("job status %q has no lifecycle event", status)
	}
	event, err := jobEventRecord(jobID, eventType, actor, "", mergePayload(result, map[string]any{
		"status": status,
		"reason": reason,
	}))
	if err != nil {
		return err
	}
	outbox, err := jobEventOutboxRecord(event)
	if err != nil {
		return err
	}
	if err := s.store.SetJobResultWithEvent(ctx, jobID, data, status, reason, outbox); err != nil {
		return s.suppressStaleTerminalTransition(ctx, jobID, status, err)
	}
	s.publishOutboxBestEffort(ctx, outbox)
	return nil
}

func jobStatusEventType(status string) string {
	switch strings.TrimSpace(status) {
	case baldastate.JobStatusCreated:
		return JobEventCreated
	case baldastate.JobStatusQueued, baldastate.JobStatusWaitingForAgent, baldastate.JobStatusWaitingForUser:
		return JobEventAssigned
	case baldastate.JobStatusRunning:
		return JobEventStarted
	case baldastate.JobStatusValidating:
		return JobEventValidating
	case baldastate.JobStatusCompleted:
		return JobEventCompleted
	case baldastate.JobStatusFailed, baldastate.JobStatusDeadLettered:
		return JobEventFailed
	case baldastate.JobStatusCanceled:
		return JobEventCanceled
	default:
		return ""
	}
}

func (s *JobService) suppressStaleTerminalTransition(ctx context.Context, jobID string, status string, err error) error {
	if err == nil {
		return nil
	}
	if !strings.Contains(err.Error(), "invalid runtime job transition") {
		return err
	}
	if !isTerminalJobStatus(status) {
		return err
	}
	job, ok, getErr := s.Get(ctx, jobID)
	if getErr != nil || !ok {
		return err
	}
	if !isTerminalJobStatus(job.Status) {
		return err
	}
	return nil
}

func isTerminalJobStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case baldastate.JobStatusCompleted,
		baldastate.JobStatusFailed,
		baldastate.JobStatusCanceled,
		baldastate.JobStatusDeadLettered:
		return true
	default:
		return false
	}
}

func (s *JobService) AppendEvent(ctx context.Context, jobID string, eventType string, actor string, messageID string, payload any) error {
	if s == nil {
		return nil
	}
	event, err := jobEventRecord(jobID, eventType, actor, messageID, payload)
	if err != nil {
		return err
	}
	outbox, err := jobEventOutboxRecord(event)
	if err != nil {
		return err
	}
	if err := s.store.EnqueueJobEvent(ctx, outbox); err != nil {
		return err
	}
	s.publishOutboxBestEffort(ctx, outbox)
	return nil
}

func jobEventRecord(jobID string, eventType string, actor string, messageID string, payload any) (baldastate.JobEventRecord, error) {
	data, err := marshalPayload(payload)
	if err != nil {
		return baldastate.JobEventRecord{}, err
	}
	eventID := ""
	if strings.TrimSpace(eventType) == JobEventAgentProgress {
		eventID = uuid.NewString()
	} else {
		parts := []string{
			strings.TrimSpace(jobID),
			strings.TrimSpace(eventType),
			strings.TrimSpace(actor),
			strings.TrimSpace(messageID),
			strings.TrimSpace(data),
		}
		sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
		eventTypePart := strings.ToLower(strings.TrimSpace(eventType))
		var eventTypeID strings.Builder
		lastDash := false
		for _, r := range eventTypePart {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				eventTypeID.WriteRune(r)
				lastDash = false
			default:
				if eventTypeID.Len() > 0 && !lastDash {
					eventTypeID.WriteByte('-')
					lastDash = true
				}
			}
			if eventTypeID.Len() >= 48 {
				break
			}
		}
		eventTypePart = strings.Trim(eventTypeID.String(), "-")
		if eventTypePart == "" {
			eventTypePart = "event"
		}
		eventID = "job:" + strings.TrimSpace(jobID) + ":event:" + eventTypePart + ":" + hex.EncodeToString(sum[:])[:16]
	}
	return baldastate.JobEventRecord{
		ID:          eventID,
		JobID:       strings.TrimSpace(jobID),
		EventType:   strings.TrimSpace(eventType),
		Actor:       strings.TrimSpace(actor),
		MessageID:   strings.TrimSpace(messageID),
		PayloadJSON: data,
	}, nil
}

func (s *JobService) CancelBySession(ctx context.Context, sessionID string, actor string, reason string) ([]string, error) {
	if s == nil {
		return nil, nil
	}
	jobs, err := s.store.ListActiveJobsBySession(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		if err := s.MarkStatus(ctx, job.ID, baldastate.JobStatusCanceled, actor, "", reason, nil); err != nil {
			return ids, err
		}
		ids = append(ids, job.ID)
	}
	return ids, nil
}

func (s *JobService) CancelJob(ctx context.Context, jobID string, actor string, reason string) error {
	if s == nil {
		return nil
	}
	return s.MarkStatus(ctx, jobID, baldastate.JobStatusCanceled, actor, "", reason, nil)
}

func (s *JobService) DeadLetter(ctx context.Context, jobID string, actor string, messageID string, reason string) error {
	return s.MarkStatus(ctx, jobID, baldastate.JobStatusDeadLettered, actor, messageID, reason, nil)
}

func (s *JobService) ReserveDelivery(ctx context.Context, record baldastate.DeliveryRecord) (baldastate.DeliveryRecord, bool, error) {
	if s == nil {
		return baldastate.DeliveryRecord{}, false, nil
	}
	return s.store.ReserveDelivery(ctx, record)
}

func (s *JobService) MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliverySent(ctx, deliveryKey, providerMessageID)
}

func (s *JobService) MarkDeliverySending(ctx context.Context, deliveryKey string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliverySending(ctx, deliveryKey)
}

func (s *JobService) MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error {
	if s == nil {
		return nil
	}
	return s.store.MarkDeliveryFailed(ctx, deliveryKey, reason)
}

func (s *JobService) ReserveAgentStep(ctx context.Context, record baldastate.AgentStepRecord) (baldastate.AgentStepRecord, bool, error) {
	if s == nil {
		return baldastate.AgentStepRecord{}, false, nil
	}
	return s.store.ReserveAgentStep(ctx, record)
}

func (s *JobService) CompleteAgentStep(ctx context.Context, stepKey string, resultJSON string) error {
	if s == nil {
		return nil
	}
	return s.store.CompleteAgentStep(ctx, stepKey, resultJSON)
}

func (s *JobService) FailAgentStep(ctx context.Context, stepKey string, resultJSON string, reason string) error {
	if s == nil {
		return nil
	}
	return s.store.FailAgentStep(ctx, stepKey, resultJSON, reason)
}

func IsGoalJob(job baldastate.JobRecord) bool {
	owner := strings.TrimSpace(job.OwnerActor)
	assigned := strings.TrimSpace(job.AssignedActor)
	for _, prefix := range []string{"goalkeeper:", "goal:"} {
		if strings.HasPrefix(owner, prefix) || strings.HasPrefix(assigned, prefix) {
			return true
		}
	}
	return false
}

func jobEventEnvelope(event baldastate.JobEventRecord) (string, actorlayer.Envelope) {
	payload := strings.TrimSpace(event.PayloadJSON)
	if payload == "" {
		payload = "{}"
	}
	subject := baldaexecution.SubjectEventJobUpdated
	switch strings.TrimSpace(event.EventType) {
	case JobEventDeliverySent:
		subject = baldaexecution.SubjectEventDeliverySent
	case JobEventDeliveryFailed:
		subject = baldaexecution.SubjectEventDeliveryFailed
	case JobEventCreated:
		subject = baldaexecution.SubjectEventJobCreated
	case JobEventCompleted:
		subject = baldaexecution.SubjectEventJobCompleted
	}
	return subject, actorlayer.Envelope{
		ID:          event.ID,
		Namespace:   baldaexecution.NamespaceTelemetry,
		Kind:        "job_event",
		From:        actorlayer.SystemAddress("job-events"),
		To:          actorlayer.ActorAddress{Target: baldaexecution.ActorTypeJob, Key: event.JobID},
		PayloadJSON: payload,
		Meta: baldaexecution.WithJobIDMeta(map[string]string{
			"event_type": event.EventType,
			"actor":      event.Actor,
			"message_id": event.MessageID,
		}, event.JobID),
	}
}

func jobEventOutboxRecord(event baldastate.JobEventRecord) (baldastate.JobEventOutboxRecord, error) {
	subject, env := jobEventEnvelope(event)
	data, err := json.Marshal(env)
	if err != nil {
		return baldastate.JobEventOutboxRecord{}, fmt.Errorf("encode job event envelope: %w", err)
	}
	return baldastate.JobEventOutboxRecord{
		ID:           strings.TrimSpace(event.ID),
		JobID:        strings.TrimSpace(event.JobID),
		Subject:      subject,
		EnvelopeJSON: string(data),
	}, nil
}

func publishOutboxRecord(
	ctx context.Context,
	store eventOutboxStore,
	bus actortransport.EventPublisher,
	record baldastate.JobEventOutboxRecord,
) error {
	if store == nil {
		return fmt.Errorf("job event outbox store is required")
	}
	if bus == nil {
		err := fmt.Errorf("event bus is required")
		return errors.Join(err, store.MarkJobEventPublishFailed(ctx, record.ID, err.Error()))
	}
	var env actorlayer.Envelope
	if err := json.Unmarshal([]byte(record.EnvelopeJSON), &env); err != nil {
		decodeErr := fmt.Errorf("decode job event outbox %q: %w", record.ID, err)
		return errors.Join(decodeErr, store.MarkJobEventPublishFailed(ctx, record.ID, decodeErr.Error()))
	}
	if err := bus.PublishEvent(ctx, record.Subject, env); err != nil {
		return errors.Join(err, store.MarkJobEventPublishFailed(ctx, record.ID, err.Error()))
	}
	return store.MarkJobEventPublished(ctx, record.ID)
}

func (s *JobService) publishOutboxBestEffort(ctx context.Context, record baldastate.JobEventOutboxRecord) {
	if s == nil {
		return
	}
	if err := publishOutboxRecord(ctx, s.store, s.bus, record); err != nil {
		log.Ctx(ctx).Warn().
			Err(err).
			Str("job_id", record.JobID).
			Str("event_id", record.ID).
			Msg("job event remains pending in outbox")
	}
}

func marshalPayload(payload any) (string, error) {
	if payload == nil {
		return "", nil
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("encode job payload: %w", err)
	}
	return string(data), nil
}

func mergePayload(payload any, extra map[string]any) any {
	out := make(map[string]any, len(extra)+1)
	if payload != nil {
		out["payload"] = payload
	}
	for key, value := range extra {
		if strings.TrimSpace(key) != "" && value != "" {
			out[key] = value
		}
	}
	return out
}
