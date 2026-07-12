package actors

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/jobexec"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"go.uber.org/fx"
)

const (
	jobPayloadKindScheduledJob       = "scheduled_job"
	jobPayloadKindWebhookSessionTurn = "session_turn"
	jobPayloadKindDelivery           = "delivery"
)

type jobEnvelopePayload struct {
	Kind         string               `json:"kind"`
	ScheduledJob *scheduledJobPayload `json:"scheduled_job,omitempty"`
	// Retain the legacy wire field for durable webhook envelopes already in flight.
	SessionTurn *SessionTurnPayload `json:"session_turn,omitempty"`
}

type scheduledJobPayload struct {
	JobID       string                       `json:"job_id"`
	Content     string                       `json:"content"`
	Locator     baldasession.SessionLocator  `json:"locator"`
	ReportTo    *baldasession.SessionLocator `json:"report_to,omitempty"`
	ParentJobID string                       `json:"parent_job_id,omitempty"`
	UserID      string                       `json:"user_id"`
	TopicID     int                          `json:"topic_id,omitempty"`
}

type DeliveryPayload = deliverycmd.Payload

type DeliveryMode = deliverycmd.Mode
type DeliveryProgress = deliverycmd.Progress
type DeliveryProgressKind = deliverycmd.ProgressKind

type jobExecutionService interface {
	DispatchWebhookSessionTurn(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload) error
	StartScheduledJob(ctx context.Context, env actorlayer.Envelope, payload jobexec.ScheduledJobRequest) error
}

const (
	DeliveryModeAgentReply DeliveryMode = deliverycmd.ModeAgentReply
	DeliveryModePlain      DeliveryMode = deliverycmd.ModePlain
	DeliveryModeMarkdown   DeliveryMode = deliverycmd.ModeMarkdown
	DeliveryModeDraftPlain DeliveryMode = deliverycmd.ModeDraftPlain
	DeliveryModeChatAction DeliveryMode = deliverycmd.ModeChatAction
	DeliveryModeProgress   DeliveryMode = deliverycmd.ModeProgress
)

const (
	DeliveryProgressThinking   DeliveryProgressKind = deliverycmd.ProgressThinking
	DeliveryProgressPlanUpdate DeliveryProgressKind = deliverycmd.ProgressPlanUpdate
)

type jobActorExecutor struct {
	tasks      jobexec.JobLifecycle
	dispatcher actortransport.Dispatcher
	sessions   *baldasession.Manager
	service    jobExecutionService
}

type jobActorExecutorParams struct {
	fx.In

	JobLifecycle jobexec.JobLifecycle
	Dispatcher   actortransport.Dispatcher
	Service      jobExecutionService
}

func WebhookJobEnvelope(payload SessionTurnPayload, routeName string, requestID string) (actorlayer.Envelope, string, error) {
	return turncmd.WebhookJobEnvelope(payload, routeName, requestID)
}

func ScheduledJobEnvelope(
	scheduledJobID string,
	content string,
	locator baldasession.SessionLocator,
	reportTo *baldasession.SessionLocator,
	userID string,
	topicID int,
	dispatchKey string,
) (actorlayer.Envelope, error) {
	return turncmd.ScheduledJobEnvelope(scheduledJobID, content, locator, reportTo, userID, topicID, dispatchKey)
}

func (e *jobActorExecutor) Address() string {
	return actorlayer.WildcardAddress(baldaexecution.ActorTypeJob)
}

func (e *jobActorExecutor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	var payload jobEnvelopePayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode job payload: %w", err))
	}
	switch strings.TrimSpace(payload.Kind) {
	case "goal":
		return actorlayer.PolicyError(fmt.Errorf("goal jobs are handled by goal actor"))
	case jobPayloadKindScheduledJob:
		if payload.ScheduledJob == nil {
			return actorlayer.PolicyError(fmt.Errorf("scheduled job payload is required"))
		}
		return e.startScheduledJob(ctx, env, *payload.ScheduledJob)
	case jobPayloadKindWebhookSessionTurn:
		if payload.SessionTurn == nil {
			return actorlayer.PolicyError(fmt.Errorf("session turn job payload is required"))
		}
		if !strings.EqualFold(env.Namespace, baldaexecution.NamespaceWebhookInbound) {
			return actorlayer.PolicyError(fmt.Errorf("session turn jobs are reserved for durable webhook delivery"))
		}
		return e.dispatchWebhookSessionTurn(ctx, env, *payload.SessionTurn)
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported job payload kind %q", payload.Kind))
	}
}

func (e *jobActorExecutor) dispatchWebhookSessionTurn(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload) error {
	if e.service == nil {
		return actorlayer.TransientError(fmt.Errorf("job execution service is required"))
	}
	return e.service.DispatchWebhookSessionTurn(ctx, env, payload)
}

func webhookJobTitle() string {
	return "Webhook job"
}

func webhookJobPriority() int {
	return 80
}

func (e *jobActorExecutor) startScheduledJob(ctx context.Context, env actorlayer.Envelope, payload scheduledJobPayload) error {
	if e.service == nil {
		return actorlayer.TransientError(fmt.Errorf("job execution service is required"))
	}
	return e.service.StartScheduledJob(ctx, env, jobexec.ScheduledJobRequest{
		JobID:       payload.JobID,
		Content:     payload.Content,
		Locator:     payload.Locator,
		ReportTo:    payload.ReportTo,
		ParentJobID: payload.ParentJobID,
		UserID:      payload.UserID,
		TopicID:     payload.TopicID,
	})
}
