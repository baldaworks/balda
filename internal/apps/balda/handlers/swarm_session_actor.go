package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"go.uber.org/fx"
)

type sessionTurnPayload struct {
	Text           string                      `json:"text"`
	Locator        baldasession.SessionLocator `json:"locator"`
	UserID         string                      `json:"user_id,omitempty"`
	AgentSessionID string                      `json:"agent_session_id,omitempty"`
	MessageID      int                         `json:"message_id,omitempty"`
	TopicID        int                         `json:"topic_id,omitempty"`
	ProgressPolicy baldachannel.ProgressPolicy `json:"progress_policy,omitempty"`
	Deliver        bool                        `json:"deliver"`
	Source         string                      `json:"source,omitempty"`
	DedupeKey      string                      `json:"dedupe_key,omitempty"`
}

func (h *BaldaHandler) submitSessionTurn(ctx context.Context, payload sessionTurnPayload) (*swarm.CommandPublishResult, error) {
	return h.submitSessionTurnToSwarm(ctx, payload)
}

func (h *BaldaHandler) submitSessionTurnToSwarm(ctx context.Context, payload sessionTurnPayload) (*swarm.CommandPublishResult, error) {
	if h.swarmCoordinator == nil || !h.swarmCoordinator.RuntimeEnabled() {
		return nil, fmt.Errorf("jetstream swarm runtime is unavailable")
	}
	env, err := sessionTurnEnvelope(payload)
	if err != nil {
		return nil, err
	}
	return h.swarmCoordinator.Submit(ctx, env)
}

func sessionTurnEnvelope(payload sessionTurnPayload) (swarm.Envelope, error) {
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		return swarm.Envelope{}, fmt.Errorf("session id is required")
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return swarm.Envelope{}, fmt.Errorf("encode session turn payload: %w", err)
	}
	source := strings.TrimSpace(payload.Source)
	if source == "" {
		source = "telegram"
	}
	priority := 90
	if strings.EqualFold(source, "webhook") {
		priority = 80
	} else if strings.EqualFold(source, "schedule") {
		priority = 50
	}
	return swarm.Envelope{
		ID:          uuid.NewString(),
		Namespace:   sessionTurnNamespace(source),
		Kind:        sessionTurnKind(source),
		From:        swarm.ActorAddress{Target: source, Key: firstNonEmpty(payload.UserID, payload.Locator.AddressKey, "unknown")},
		To:          swarm.ActorAddress{Target: swarm.ActorTypeSession, Key: payload.Locator.SessionID},
		SessionID:   payload.Locator.SessionID,
		Priority:    priority,
		DedupeKey:   strings.TrimSpace(payload.DedupeKey),
		PayloadJSON: string(data),
	}, nil
}

func (h *BaldaHandler) runSessionTurnPayload(ctx context.Context, payload sessionTurnPayload) error {
	ts, err := h.sessionManager.GetSession(payload.Locator)
	if err != nil {
		h.logger.Debug().
			Str("session_id", payload.Locator.SessionID).
			Str("address_key", payload.Locator.AddressKey).
			Msg("dropping queued turn for inactive session")
		return nil
	}
	userID := strings.TrimSpace(payload.UserID)
	if userID == "" {
		userID = ts.GetUserID()
	}
	agentSessionID := strings.TrimSpace(payload.AgentSessionID)
	if agentSessionID == "" {
		agentSessionID = ts.GetAgentSessionID()
	}
	return h.runTurnTaskWithDelivery(
		ctx,
		payload.Text,
		ts.GetRunner(),
		userID,
		ts.GetSessionID(),
		agentSessionID,
		payload.Locator,
		payload.MessageID,
		payload.TopicID,
		payload.ProgressPolicy,
		payload.Deliver,
	)
}

type sessionActorExecutor struct {
	handler *BaldaHandler
	tasks   *swarm.TaskService
}

type sessionActorExecutorParams struct {
	fx.In

	Handler *BaldaHandler
	Tasks   *swarm.TaskService `optional:"true"`
}

func newSessionActorExecutor(params sessionActorExecutorParams) swarm.Actor {
	return &sessionActorExecutor{handler: params.Handler, tasks: params.Tasks}
}

func (e *sessionActorExecutor) Address() string {
	return swarm.WildcardAddress(swarm.ActorTypeSession)
}

func (e *sessionActorExecutor) Handle(ctx context.Context, env swarm.Envelope) error {
	switch strings.TrimSpace(env.Namespace) {
	case swarm.NamespaceHumanInbound, swarm.NamespaceWebhookInbound, swarm.NamespaceScheduleInbound, swarm.NamespaceAgentCommand, swarm.NamespaceTaskControl:
		return e.enqueueTurn(ctx, env)
	default:
		return swarm.PolicyError(fmt.Errorf("unsupported session namespace %q", env.Namespace))
	}
}

func (e *sessionActorExecutor) enqueueTurn(ctx context.Context, env swarm.Envelope) error {
	var payload sessionTurnPayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return swarm.PermanentError(fmt.Errorf("decode session turn payload: %w", err))
	}
	if strings.TrimSpace(payload.Locator.SessionID) == "" {
		payload.Locator.SessionID = strings.TrimSpace(env.To.Key)
	}
	if e.handler.turnDispatcher == nil {
		return swarm.TransientError(fmt.Errorf("turn dispatcher is required"))
	}
	if swarm.QueueModeOf(env) == swarm.QueueModeInterrupt {
		_, _, err := e.handler.turnDispatcher.CancelSession(payload.Locator, true)
		if err != nil {
			return swarm.TransientError(fmt.Errorf("interrupt session turn: %w", err))
		}
	}

	done := make(chan error, 1)
	_, err := e.handler.turnDispatcher.Enqueue(TurnTask{
		SessionID: payload.Locator.SessionID,
		Run: func(runCtx context.Context) error {
			err := e.handler.runSessionTurnPayload(runCtx, payload)
			done <- err
			return err
		},
	})
	if err != nil {
		if errors.Is(err, ErrTurnQueueFull) {
			return swarm.TransientError(err)
		}
		return swarm.TransientError(fmt.Errorf("enqueue session actor turn: %w", err))
	}

	select {
	case err := <-done:
		e.recordSessionTaskResult(ctx, env, err)
		return err
	case <-ctx.Done():
		return swarm.TransientError(ctx.Err())
	}
}

func (e *sessionActorExecutor) recordSessionTaskResult(ctx context.Context, env swarm.Envelope, runErr error) {
	if e == nil || e.tasks == nil || strings.TrimSpace(env.TaskID) == "" {
		return
	}
	if runErr == nil {
		_ = e.tasks.MarkStatus(ctx, env.TaskID, baldastate.SwarmTaskStatusCompleted, "session.actor", env.ID, "", map[string]any{
			"namespace": env.Namespace,
			"kind":      env.Kind,
		})
	}
}

func sessionTurnNamespace(source string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "webhook":
		return swarm.NamespaceWebhookInbound
	case "schedule":
		return swarm.NamespaceScheduleInbound
	case "agent":
		return swarm.NamespaceAgentCommand
	default:
		return swarm.NamespaceHumanInbound
	}
}

func sessionTurnKind(source string) string {
	if strings.EqualFold(strings.TrimSpace(source), "webhook") {
		return swarm.KindWebhookEvent
	}
	return swarm.KindMessage
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
