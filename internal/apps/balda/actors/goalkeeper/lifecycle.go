package goalkeeper

import (
	"context"
	"errors"
	"fmt"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/goaldelivery"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/baldaworks/go-actorlayer"
)

// lifecycle.go owns goal job/session lifecycle helpers used by the actor coordinator.
type sessionAccessor interface {
	GetSession(locator baldasession.SessionLocator) (*baldasession.TopicSession, error)
	RestoreSession(ctx context.Context, sessionCtx baldasession.SessionContext) (*baldasession.TopicSession, error)
	EnsureSession(ctx context.Context, sessionCtx baldasession.SessionContext, agentName string) (*baldasession.TopicSession, error)
}

type baldaSessionAccessor struct {
	manager *baldasession.Manager
}

func newSessionAccessor(manager *baldasession.Manager) sessionAccessor {
	if manager == nil {
		return nil
	}
	return baldaSessionAccessor{manager: manager}
}

func (a baldaSessionAccessor) GetSession(locator baldasession.SessionLocator) (*baldasession.TopicSession, error) {
	return a.manager.GetSession(locator)
}

func (a baldaSessionAccessor) RestoreSession(ctx context.Context, sessionCtx baldasession.SessionContext) (*baldasession.TopicSession, error) {
	return a.manager.RestoreSession(ctx, sessionCtx)
}

func (a baldaSessionAccessor) EnsureSession(ctx context.Context, sessionCtx baldasession.SessionContext, agentName string) (*baldasession.TopicSession, error) {
	return a.manager.EnsureSession(ctx, sessionCtx, agentName)
}

func (c *coordinator) ensureGoalJob(ctx context.Context, payload goalJobPayload) error {
	if c.jobs == nil {
		return actorlayer.TransientError(fmt.Errorf("job service is required"))
	}
	title := strings.TrimSpace(payload.Objective)
	if title != "" {
		const maxTitleRunes = 80
		runes := []rune(title)
		if len(runes) > maxTitleRunes {
			title = strings.TrimSpace(string(runes[:maxTitleRunes])) + "..."
		}
		title = "Goal: " + title
	} else {
		title = "Goal"
	}
	record := baldastate.JobRecord{
		ID:            strings.TrimSpace(payload.JobID),
		SessionID:     strings.TrimSpace(payload.Locator.SessionID),
		Title:         title,
		Objective:     strings.TrimSpace(payload.Objective),
		Status:        baldastate.JobStatusCreated,
		OwnerActor:    baldaexecution.ActorTypeGoalkeeper + ":" + strings.TrimSpace(payload.JobID),
		AssignedActor: baldaexecution.ActorTypeGoalkeeper + ":" + strings.TrimSpace(payload.JobID),
		Priority:      90,
		CreatedBy:     strings.TrimSpace(payload.TransportUserID),
	}
	if _, err := c.jobs.Create(ctx, record, actorName, payload); err != nil {
		return actorlayer.TransientError(err)
	}
	task, ok, err := c.jobs.Get(ctx, payload.JobID)
	if err != nil {
		return actorlayer.TransientError(err)
	}
	if !ok {
		return actorlayer.TransientError(fmt.Errorf("goal job %q was not persisted", payload.JobID))
	}
	switch strings.TrimSpace(task.Status) {
	case "", baldastate.JobStatusCreated, baldastate.JobStatusQueued:
		return c.jobs.MarkStatus(ctx, payload.JobID, baldastate.JobStatusQueued, actorName, "", "", nil)
	default:
		return nil
	}
}

func (c *coordinator) ensureNoOtherActiveGoal(ctx context.Context, jobID string, payload goalJobPayload) (bool, error) {
	if c == nil || c.jobs == nil {
		return false, actorlayer.TransientError(fmt.Errorf("job service is required"))
	}
	progressEmitter := newGoalProgressEmitter(c.jobs, c.events, c.dispatcher)
	outcomes := newGoalOutcomeAssembler(c.jobs)
	activeGoals, err := c.jobs.ListActiveGoalJobsBySession(ctx, payload.Locator.SessionID)
	if err != nil {
		return false, actorlayer.TransientError(fmt.Errorf("list active goal jobs: %w", err))
	}
	for _, task := range activeGoals {
		if strings.TrimSpace(task.ID) == strings.TrimSpace(jobID) {
			continue
		}
		reason := "another goal run is already active for this session"
		if setErr := c.jobs.SetResult(ctx, jobID, outcomes.toJobResult(goalRunResult{payload: payload}, false, goalArtifactSnapshot{}, &goalExportResultV1{
			Status: "canceled",
			Error:  reason,
		}), baldastate.JobStatusCanceled, actorName, reason); setErr != nil {
			return false, actorlayer.TransientError(setErr)
		}
		if err := progressEmitter.deliver(ctx, jobID, payload, goaldelivery.RenderStatusMessage(goalDeliveryProfile(payload), "A goal run is already active for this session."), "already-active"); err != nil {
			return false, err
		}
		return true, nil
	}
	return false, nil
}

func (c *coordinator) resolveSession(ctx context.Context, payload goalJobPayload) (*baldasession.TopicSession, error) {
	if c.sessionAccessor == nil {
		return nil, fmt.Errorf("session manager is required")
	}
	ts, err := c.sessionAccessor.GetSession(payload.Locator)
	if err == nil {
		return ts, nil
	}
	userID := strings.TrimSpace(payload.TransportUserID)
	if userID == "" {
		return nil, fmt.Errorf("restore user id is required")
	}
	sessionCtx := baldasession.SessionContext{Locator: payload.Locator, UserID: userID}
	ts, err = c.sessionAccessor.RestoreSession(ctx, sessionCtx)
	if err == nil {
		return ts, nil
	}
	if !errors.Is(err, baldasession.ErrNoPersistedSession) {
		return nil, fmt.Errorf("restore session for goal: %w", err)
	}
	ts, err = c.sessionAccessor.EnsureSession(ctx, sessionCtx, ownerSessionLabel)
	if err != nil {
		return nil, fmt.Errorf("create session for goal: %w", err)
	}
	return ts, nil
}
