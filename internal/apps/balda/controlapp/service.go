package controlapp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/controlcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type Service struct {
	turnDispatcher appports.TurnQueue
	dispatcher     actortransport.Dispatcher
	jobs           JobLifecycle
	scheduledJobs  ScheduledJobs
	jobRuns        JobRuns
	logger         zerolog.Logger
}

func New(turns appports.TurnQueue, dispatcher actortransport.Dispatcher, jobs JobLifecycle, scheduled ScheduledJobs, jobRuns JobRuns, logger zerolog.Logger) *Service {
	return &Service{turnDispatcher: turns, dispatcher: dispatcher, jobs: jobs, scheduledJobs: scheduled, jobRuns: jobRuns, logger: logger}
}

func (s *Service) CancelJob(ctx context.Context, payload controlcmd.Payload) error {
	jobID := strings.TrimSpace(payload.JobID)
	if jobID == "" {
		return actorlayer.PolicyError(fmt.Errorf("job id is required"))
	}
	if s.jobs == nil {
		return actorlayer.TransientError(fmt.Errorf("job service is required"))
	}
	job, ok, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	if !ok {
		if payload.Notify {
			s.sendControlMessage(ctx, payload.Locator, fmt.Sprintf("Job %q not found.", jobID))
		}
		return nil
	}
	if isTerminalJobStatus(job.Status) {
		if payload.Notify {
			s.sendControlMessage(ctx, payload.Locator, fmt.Sprintf("Job %s is already %s.", job.ID, job.Status))
		}
		return nil
	}
	runCanceled := false
	if s.jobRuns != nil {
		runCanceled = s.jobRuns.Cancel(job.ID)
	}
	if !runCanceled && s.turnDispatcher != nil && strings.TrimSpace(payload.Locator.SessionID) != "" {
		hadInFlight, dropped, err := s.turnDispatcher.CancelSession(payload.Locator, true)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		runCanceled = hadInFlight || dropped > 0
	}
	reason := firstNonEmpty(payload.Reason, "job canceled by user")
	if err := s.jobs.CancelJob(ctx, job.ID, "command.job", reason); err != nil {
		return actorlayer.TransientError(err)
	}
	if payload.Notify {
		s.sendControlMessage(ctx, payload.Locator, fmt.Sprintf("Canceled job %s. Active run canceled: %t.", job.ID, runCanceled))
	}
	return nil
}

func (s *Service) CancelSession(ctx context.Context, payload controlcmd.Payload) error {
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("session id is required"))
	}
	hadInFlight := false
	dropped := 0
	if s.turnDispatcher != nil {
		var err error
		hadInFlight, dropped, err = s.turnDispatcher.CancelSession(payload.Locator, true)
		if err != nil {
			return actorlayer.TransientError(err)
		}
	}
	jobCanceled := 0
	if s.jobs != nil {
		jobIDs, err := s.jobs.CancelBySession(ctx, payload.Locator.SessionID, "command.cancel", firstNonEmpty(payload.Reason, "session canceled by user"))
		if err != nil {
			return actorlayer.TransientError(err)
		}
		for _, jobID := range jobIDs {
			if s.jobRuns != nil && s.jobRuns.Cancel(jobID) {
				jobCanceled++
			}
		}
	}
	if payload.Notify {
		response := "Canceled current turn."
		if !hadInFlight && dropped == 0 && jobCanceled == 0 {
			response = "No running or queued session work."
		} else if !hadInFlight {
			response = "No running turn to cancel."
		}
		if dropped > 0 {
			response += fmt.Sprintf("\nDropped %d queued session message(s).", dropped)
		}
		if jobCanceled > 0 {
			response += fmt.Sprintf("\nCanceled %d active task(s).", jobCanceled)
		}
		s.sendControlMessage(ctx, payload.Locator, response)
	}
	return nil
}

func (s *Service) CancelSessionTurn(ctx context.Context, payload controlcmd.Payload) error {
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("session id is required"))
	}
	hadInFlight := false
	dropped := 0
	if s.turnDispatcher != nil {
		var err error
		hadInFlight, dropped, err = s.turnDispatcher.CancelSession(payload.Locator, true)
		if err != nil {
			return actorlayer.TransientError(err)
		}
	}
	if payload.Notify {
		response := "Canceled current turn."
		if !hadInFlight && dropped == 0 {
			response = "No running or queued session work."
		} else if !hadInFlight {
			response = "No running turn to cancel."
		}
		if dropped > 0 {
			response += fmt.Sprintf("\nDropped %d queued session message(s).", dropped)
		}
		s.sendControlMessage(ctx, payload.Locator, response)
	}
	return nil
}

func (s *Service) ClearGoal(ctx context.Context, payload controlcmd.Payload) error {
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("session id is required"))
	}
	if s.jobs == nil {
		return actorlayer.TransientError(fmt.Errorf("job service is required"))
	}
	jobs, err := s.jobs.ListActiveGoalJobsBySession(ctx, payload.Locator.SessionID)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	cleared := 0
	for _, job := range jobs {
		if err := s.jobs.CancelJob(ctx, job.ID, "command.goal", firstNonEmpty(payload.Reason, "goal cleared by user")); err != nil {
			return actorlayer.TransientError(err)
		}
		if s.jobRuns != nil {
			s.jobRuns.Cancel(job.ID)
		}
		cleared++
	}
	if payload.Notify {
		switch cleared {
		case 0:
			s.sendControlMessage(ctx, payload.Locator, "No active goal run.")
		case 1:
			s.sendControlMessage(ctx, payload.Locator, "Cleared active goal run.")
		default:
			s.sendControlMessage(ctx, payload.Locator, fmt.Sprintf("Cleared %d active goal runs.", cleared))
		}
	}
	return nil
}

func (s *Service) ScheduleWait(ctx context.Context, payload controlcmd.Payload) error {
	if s.scheduledJobs == nil {
		return actorlayer.TransientError(fmt.Errorf("scheduled job store is required"))
	}
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("session id is required"))
	}
	if payload.Wait == nil {
		return actorlayer.PolicyError(fmt.Errorf("wait payload is required"))
	}
	waitID := strings.TrimSpace(payload.Wait.JobID)
	if waitID == "" {
		waitID = "wait-" + uuid.NewString()
	}
	content := strings.TrimSpace(payload.Wait.Content)
	if content == "" {
		return actorlayer.PolicyError(fmt.Errorf("wait content is required"))
	}
	delaySeconds := payload.Wait.DelaySeconds
	if delaySeconds <= 0 {
		return actorlayer.PolicyError(fmt.Errorf("wait delay_seconds must be positive"))
	}
	now := time.Now().UTC()
	record := baldastate.ScheduledJobRecord{
		JobID:        waitID,
		SessionID:    strings.TrimSpace(payload.Locator.SessionID),
		ChannelType:  strings.TrimSpace(payload.Locator.ChannelType),
		AddressKey:   strings.TrimSpace(payload.Locator.AddressKey),
		AddressJSON:  strings.TrimSpace(payload.Locator.AddressJSON),
		Content:      content,
		ScheduleSpec: "@once",
		Timezone:     "UTC",
		Status:       baldastate.ScheduledJobStatusActive,
		MaxRetries:   0,
		NextRunAt:    now.Add(time.Duration(delaySeconds) * time.Second),
	}
	if payload.Wait.ReportToSelf {
		record.ReportToEnabled = true
		record.ReportToSessionID = record.SessionID
		record.ReportToChannelType = record.ChannelType
		record.ReportToAddressKey = record.AddressKey
		record.ReportToAddressJSON = record.AddressJSON
	}
	if err := s.scheduledJobs.Upsert(ctx, record); err != nil {
		return actorlayer.TransientError(err)
	}
	if payload.Notify {
		s.sendControlMessage(ctx, payload.Locator, fmt.Sprintf("Scheduled wait %s for %ds.", waitID, delaySeconds))
	}
	return nil
}

func (s *Service) sendControlMessage(ctx context.Context, locator baldasession.SessionLocator, text string) {
	if s == nil || s.dispatcher == nil || strings.TrimSpace(text) == "" {
		return
	}
	env, err := deliverycmd.PlainEnvelopeWithSettlement("", actorlayer.SystemAddress("control"), locator, deliverycmd.SettlementBypass, text, "")
	if err != nil {
		s.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to build control response")
		return
	}
	if _, err := s.dispatcher.Dispatch(ctx, env); err != nil {
		s.logger.Warn().Err(err).Str("session_id", locator.SessionID).Msg("failed to send control response")
	}
}

func isTerminalJobStatus(status string) bool {
	switch strings.TrimSpace(status) {
	case baldastate.JobStatusCompleted, baldastate.JobStatusFailed, baldastate.JobStatusCanceled, baldastate.JobStatusDeadLettered:
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
