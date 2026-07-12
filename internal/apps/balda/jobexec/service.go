package jobexec

import (
	"context"
	"fmt"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
)

type Service struct {
	tasks      JobLifecycle
	dispatcher actortransport.Dispatcher
}

type ScheduledJobRequest struct {
	JobID       string
	Content     string
	Locator     baldasession.SessionLocator
	ReportTo    *baldasession.SessionLocator
	ParentJobID string
	UserID      string
	TopicID     int
}

func New(tasks JobLifecycle, dispatcher actortransport.Dispatcher) *Service {
	return &Service{tasks: tasks, dispatcher: dispatcher}
}

func (s *Service) DispatchWebhookSessionTurn(ctx context.Context, env actorlayer.Envelope, payload turncmd.SessionTurnPayload) error {
	jobID := strings.TrimSpace(baldaexecution.EnvelopeJobID(env))
	if jobID == "" {
		return actorlayer.PolicyError(fmt.Errorf("job id is required"))
	}
	if payloadJobID := strings.TrimSpace(payload.JobID); payloadJobID != "" && payloadJobID != jobID {
		return actorlayer.PolicyError(fmt.Errorf("webhook session job id mismatch: envelope=%q payload=%q", jobID, payloadJobID))
	}
	if s.tasks != nil {
		if _, ok, err := s.tasks.Get(ctx, jobID); err != nil {
			return actorlayer.TransientError(err)
		} else if ok {
			return nil
		}
		created, err := s.tasks.Create(ctx, baldastate.JobRecord{
			ID:            jobID,
			SessionID:     strings.TrimSpace(payload.Locator.SessionID),
			ParentJobID:   strings.TrimSpace(payload.ParentJobID),
			Title:         "Webhook job",
			Objective:     strings.TrimSpace(payload.Text),
			Status:        baldastate.JobStatusCreated,
			OwnerActor:    baldaexecution.ActorTypeJob + ":" + jobID,
			AssignedActor: baldaexecution.ActorTypeSession + ":" + payload.Locator.SessionID,
			Priority:      80,
			CreatedBy:     strings.TrimSpace(payload.UserID),
		}, "job.actor", payload)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if !created {
			return nil
		}
	}
	payload.JobID = jobID
	sessionEnv, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		return actorlayer.PermanentError(err)
	}
	sessionEnv.CorrelationID = firstNonEmpty(env.CorrelationID, jobID)
	sessionEnv.CausationID = env.ID
	if strings.TrimSpace(sessionEnv.DedupeKey) != "" {
		sessionEnv.ID = sessionEnv.DedupeKey
	}
	if _, err := s.dispatcher.Dispatch(ctx, sessionEnv); err != nil {
		return actorlayer.TransientError(err)
	}
	if s.tasks != nil {
		if err := s.tasks.MarkStatus(ctx, jobID, baldastate.JobStatusRunning, "job.actor", env.ID, "", nil); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	return nil
}

func (s *Service) StartScheduledJob(ctx context.Context, env actorlayer.Envelope, payload ScheduledJobRequest) error {
	jobID := strings.TrimSpace(baldaexecution.EnvelopeJobID(env))
	content := strings.TrimSpace(payload.Content)
	if jobID == "" {
		return actorlayer.PolicyError(fmt.Errorf("job id is required"))
	}
	if strings.TrimSpace(payload.JobID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("scheduled job id is required"))
	}
	if content == "" {
		return actorlayer.PolicyError(fmt.Errorf("scheduled job content is required"))
	}
	if s.tasks != nil {
		if _, ok, err := s.tasks.Get(ctx, jobID); err != nil {
			return actorlayer.TransientError(err)
		} else if ok {
			return nil
		}
		created, err := s.tasks.Create(ctx, baldastate.JobRecord{
			ID:            jobID,
			SessionID:     strings.TrimSpace(payload.Locator.SessionID),
			ParentJobID:   strings.TrimSpace(payload.ParentJobID),
			Title:         "Scheduled job: " + strings.TrimSpace(payload.JobID),
			Objective:     content,
			Status:        baldastate.JobStatusCreated,
			OwnerActor:    baldaexecution.ActorTypeJob + ":" + jobID,
			AssignedActor: baldaexecution.ActorTypeSession + ":" + payload.Locator.SessionID,
			Priority:      50,
			CreatedBy:     strings.TrimSpace(payload.UserID),
		}, "job.actor", payload)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if !created {
			return nil
		}
	}
	sessionPayload := turncmd.SessionTurnPayload{
		JobID:          jobID,
		Text:           content,
		Locator:        payload.Locator,
		ReportTo:       payload.ReportTo,
		ParentJobID:    strings.TrimSpace(payload.ParentJobID),
		UserID:         payload.UserID,
		ScheduledJobID: payload.JobID,
		TopicID:        payload.TopicID,
		DeliveryOptions: deliveryfmt.Options{
			Profile: deliveryfmt.Profile{Format: deliveryfmt.FormatAuto},
		},
		Deliver:   payload.ReportTo != nil,
		Source:    turncmd.SourceSchedule,
		DedupeKey: firstNonEmpty(env.DedupeKey, jobID) + ":session",
	}
	sessionEnv, err := turncmd.SessionTurnEnvelope(sessionPayload)
	if err != nil {
		return actorlayer.PermanentError(err)
	}
	sessionEnv.CorrelationID = firstNonEmpty(env.CorrelationID, jobID)
	sessionEnv.CausationID = env.ID
	if strings.TrimSpace(sessionEnv.DedupeKey) != "" {
		sessionEnv.ID = sessionEnv.DedupeKey
	}
	if _, err := s.dispatcher.Dispatch(ctx, sessionEnv); err != nil {
		return actorlayer.TransientError(err)
	}
	if s.tasks != nil {
		if err := s.tasks.MarkStatus(ctx, jobID, baldastate.JobStatusRunning, "job.actor", env.ID, "", nil); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
