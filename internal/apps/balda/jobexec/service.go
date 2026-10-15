package jobexec

import (
	"context"
	"fmt"
	"strings"

	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

type Service struct {
	tasks      JobLifecycle
	dispatcher actortransport.Dispatcher
	modes      appports.ScheduleModeResolver
}

type ScheduledJobRequest struct {
	JobID       string
	Content     string
	Locator     baldasession.SessionLocator
	ReportTo    *baldasession.SessionLocator
	ParentJobID string
	UserID      string
	TopicID     int
	OneShot     *bool
}

func New(tasks JobLifecycle, dispatcher actortransport.Dispatcher) *Service {
	return &Service{tasks: tasks, dispatcher: dispatcher}
}

func NewWithScheduleModes(tasks JobLifecycle, dispatcher actortransport.Dispatcher, modes appports.ScheduleModeResolver) *Service {
	return &Service{tasks: tasks, dispatcher: dispatcher, modes: modes}
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
		existing, found, err := s.tasks.Get(ctx, jobID)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if !found {
			created, err := s.tasks.Create(ctx, baldastate.JobRecord{
				ID:             jobID,
				SessionID:      strings.TrimSpace(payload.Locator.SessionID),
				ParentJobID:    strings.TrimSpace(payload.ParentJobID),
				Title:          "Webhook job",
				Objective:      strings.TrimSpace(payload.Text),
				Status:         baldastate.JobStatusCreated,
				OwnerActor:     baldaexecution.ActorTypeJob + ":" + jobID,
				AssignedActor:  baldaexecution.ActorTypeSession + ":" + payload.Locator.SessionID,
				Priority:       80,
				CreatedBy:      strings.TrimSpace(payload.UserID),
				PrivateRunKind: baldastate.PrivateRunKindWebhook,
			}, "job.actor", payload)
			if err != nil {
				return actorlayer.TransientError(err)
			}
			if !created {
				existing, found, err = s.tasks.Get(ctx, jobID)
				if err != nil {
					return actorlayer.TransientError(fmt.Errorf("load concurrent webhook job: %w", err))
				}
				if !found {
					return actorlayer.TransientError(fmt.Errorf("concurrent webhook job is unavailable"))
				}
			} else {
				existing = baldastate.JobRecord{SessionID: payload.Locator.SessionID, Status: baldastate.JobStatusCreated}
			}
		}
		if terminalJobExecution(existing.Status) || webhookSessionAlreadyDispatched(existing.Status) {
			return nil
		}
		if existing.SessionID != payload.Locator.SessionID {
			return actorlayer.PolicyError(fmt.Errorf("webhook job session mismatch"))
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
			if current, found, getErr := s.tasks.Get(ctx, jobID); getErr == nil && found && terminalJobExecution(current.Status) {
				return nil
			}
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
	oneShot := payload.OneShot != nil && *payload.OneShot
	if payload.OneShot == nil {
		if s.modes == nil {
			return actorlayer.TransientError(fmt.Errorf("scheduled mode resolver is unavailable"))
		}
		var err error
		oneShot, err = s.modes.IsOneShot(ctx, payload.JobID)
		if err != nil {
			return actorlayer.TransientError(err)
		}
	}
	executionLocator := payload.Locator
	reportTo := payload.ReportTo
	if !oneShot {
		privateTurn, err := turncmd.PrivateScheduledTurn(turncmd.SessionTurnPayload{
			JobID: jobID, ScheduledJobID: payload.JobID, Locator: payload.Locator, ReportTo: payload.ReportTo,
		})
		if err != nil {
			return actorlayer.PolicyError(err)
		}
		executionLocator, reportTo = privateTurn.Locator, privateTurn.ReportTo
		payload.UserID = privateTurn.UserID
	}
	markRunning := true
	if s.tasks != nil {
		if existing, ok, err := s.tasks.Get(ctx, jobID); err != nil {
			return actorlayer.TransientError(err)
		} else if ok {
			if terminalJobExecution(existing.Status) {
				return nil
			}
			if !oneShot && existing.SessionID != executionLocator.SessionID {
				if err := s.rebindScheduledSession(ctx, jobID, existing.SessionID, executionLocator.SessionID); err != nil {
					return actorlayer.TransientError(err)
				}
			}
			markRunning = existing.Status == baldastate.JobStatusCreated || existing.Status == baldastate.JobStatusQueued
		} else {
			created, err := s.tasks.Create(ctx, baldastate.JobRecord{
				ID:            jobID,
				SessionID:     executionLocator.SessionID,
				ParentJobID:   strings.TrimSpace(payload.ParentJobID),
				Title:         "Scheduled job: " + strings.TrimSpace(payload.JobID),
				Objective:     content,
				Status:        baldastate.JobStatusCreated,
				OwnerActor:    baldaexecution.ActorTypeJob + ":" + jobID,
				AssignedActor: baldaexecution.ActorTypeSession + ":" + executionLocator.SessionID,
				Priority:      50,
				CreatedBy:     strings.TrimSpace(payload.UserID),
			}, "job.actor", payload)
			if err != nil {
				return actorlayer.TransientError(err)
			}
			if !created {
				existing, found, err := s.tasks.Get(ctx, jobID)
				if err != nil {
					return actorlayer.TransientError(fmt.Errorf("load concurrent scheduled execution: %w", err))
				}
				if !found {
					return actorlayer.TransientError(fmt.Errorf("concurrent scheduled execution is unavailable"))
				}
				if terminalJobExecution(existing.Status) {
					return nil
				}
				if !oneShot && existing.SessionID != executionLocator.SessionID {
					if err := s.rebindScheduledSession(ctx, jobID, existing.SessionID, executionLocator.SessionID); err != nil {
						return actorlayer.TransientError(err)
					}
				}
				markRunning = existing.Status == baldastate.JobStatusCreated || existing.Status == baldastate.JobStatusQueued
			}
		}
	}
	sessionPayload := turncmd.SessionTurnPayload{
		JobID:           jobID,
		Text:            content,
		Locator:         executionLocator,
		ReportTo:        reportTo,
		ParentJobID:     strings.TrimSpace(payload.ParentJobID),
		UserID:          payload.UserID,
		ScheduledJobID:  payload.JobID,
		ScheduleOneShot: &oneShot,
		TopicID:         payload.TopicID,
		DeliveryFormat:  "",
		Deliver:         reportTo != nil,
		Source:          turncmd.SourceSchedule,
		DedupeKey:       firstNonEmpty(env.DedupeKey, jobID) + ":session",
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
	if s.tasks != nil && markRunning {
		if err := s.tasks.MarkStatus(ctx, jobID, baldastate.JobStatusRunning, "job.actor", env.ID, "", nil); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	return nil
}

func (s *Service) rebindScheduledSession(ctx context.Context, jobID, oldSessionID, privateSessionID string) error {
	if strings.TrimSpace(oldSessionID) == "" {
		return fmt.Errorf("scheduled job %q has no prior session scope", jobID)
	}
	updated, err := s.tasks.RebindScheduledSession(ctx, jobID, oldSessionID, privateSessionID)
	if err != nil {
		return fmt.Errorf("rebind scheduled job %q: %w", jobID, err)
	}
	if !updated {
		return fmt.Errorf("scheduled job %q scope changed concurrently", jobID)
	}
	return nil
}

func terminalJobExecution(status string) bool {
	switch status {
	case baldastate.JobStatusCompleted, baldastate.JobStatusFailed,
		baldastate.JobStatusCanceled, baldastate.JobStatusDeadLettered:
		return true
	default:
		return false
	}
}

func webhookSessionAlreadyDispatched(status string) bool {
	switch status {
	case baldastate.JobStatusRunning, baldastate.JobStatusWaitingForAgent,
		baldastate.JobStatusWaitingForUser, baldastate.JobStatusValidating:
		return true
	default:
		return false
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
