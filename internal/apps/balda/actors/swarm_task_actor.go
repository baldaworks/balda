package actors

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	baldajobs "github.com/normahq/balda/internal/apps/balda/jobs"
	baldaruntime "github.com/normahq/balda/internal/apps/balda/runtime"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"go.uber.org/fx"
)

const (
	taskPayloadKindScheduledTask = "scheduled_task"
	// Retain the legacy wire kind for durable webhook envelopes already in flight.
	taskPayloadKindWebhookSessionTurn = "session_turn"
	taskPayloadKindDelivery           = "delivery"
)

type taskEnvelopePayload struct {
	Kind          string                `json:"kind"`
	ScheduledTask *scheduledTaskPayload `json:"scheduled_task,omitempty"`
	// Retain the legacy wire field for durable webhook envelopes already in flight.
	SessionTurn *SessionTurnPayload `json:"session_turn,omitempty"`
}

type scheduledTaskPayload struct {
	TaskID       string                       `json:"task_id"`
	Content      string                       `json:"content"`
	Locator      baldasession.SessionLocator  `json:"locator"`
	ReportTo     *baldasession.SessionLocator `json:"report_to,omitempty"`
	ParentTaskID string                       `json:"parent_task_id,omitempty"`
	UserID       string                       `json:"user_id"`
	TopicID      int                          `json:"topic_id,omitempty"`
}

type DeliveryPayload = deliverycmd.Payload

type DeliveryMode = deliverycmd.Mode
type DeliveryProgress = deliverycmd.Progress
type DeliveryProgressKind = deliverycmd.ProgressKind

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

type taskActorExecutor struct {
	tasks      *baldajobs.JobService
	dispatcher actortransport.Dispatcher
	sessions   *baldasession.Manager
}

type taskActorExecutorParams struct {
	fx.In

	JobService *baldajobs.JobService
	Dispatcher actortransport.Dispatcher
	Sessions   *baldasession.Manager `optional:"true"`
}

func WebhookTaskEnvelope(payload SessionTurnPayload, routeName string, requestID string) (actorlayer.Envelope, string, error) {
	dedupeBase := strings.TrimSpace(payload.DedupeKey)
	dedupeBase = strings.TrimSuffix(dedupeBase, ":task")
	dedupeBase = strings.TrimSuffix(dedupeBase, ":session")
	if dedupeBase == "" {
		dedupeBase = strings.Join([]string{"webhook", strings.TrimSpace(routeName), strings.TrimSpace(requestID)}, ":")
	}
	trimmedRoute := strings.ToLower(strings.TrimSpace(routeName))
	var routePart strings.Builder
	lastDash := false
	for _, r := range trimmedRoute {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			routePart.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_':
			routePart.WriteRune(r)
			lastDash = false
		default:
			if routePart.Len() > 0 && !lastDash {
				routePart.WriteByte('-')
				lastDash = true
			}
		}
		if routePart.Len() >= 48 {
			break
		}
	}
	part := strings.Trim(routePart.String(), "-_")
	if part == "" {
		part = "inbound"
	}
	taskID := "webhook-" + part + "-" + shortTaskHash(dedupeBase)
	payload.DedupeKey = dedupeBase + ":session"
	data, err := json.Marshal(taskEnvelopePayload{
		Kind:        taskPayloadKindWebhookSessionTurn,
		SessionTurn: &payload,
	})
	if err != nil {
		return actorlayer.Envelope{}, "", fmt.Errorf("encode webhook task payload: %w", err)
	}
	return actorlayer.Envelope{
		ID:          uuid.NewString(),
		Namespace:   baldaruntime.NamespaceWebhookInbound,
		Kind:        baldaruntime.KindWebhookEvent,
		From:        actorlayer.ActorAddress{Target: "webhook", Key: firstNonEmpty(routeName, requestID, "inbound")},
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeTask, Key: taskID},
		SessionID:   payload.Locator.SessionID,
		TaskID:      taskID,
		Priority:    80,
		DedupeKey:   dedupeBase + ":task",
		PayloadJSON: string(data),
	}, taskID, nil
}

func ScheduledTaskEnvelope(
	scheduledTaskID string,
	content string,
	locator baldasession.SessionLocator,
	reportTo *baldasession.SessionLocator,
	userID string,
	topicID int,
	dispatchKey string,
) (actorlayer.Envelope, error) {
	payload := taskEnvelopePayload{
		Kind: taskPayloadKindScheduledTask,
		ScheduledTask: &scheduledTaskPayload{
			TaskID:   strings.TrimSpace(scheduledTaskID),
			Content:  content,
			Locator:  locator,
			ReportTo: reportTo,
			UserID:   strings.TrimSpace(userID),
			TopicID:  topicID,
		},
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode scheduled task task: %w", err)
	}
	taskID := "scheduled-" + strings.TrimSpace(scheduledTaskID) + "-" + strings.TrimSpace(dispatchKey)
	return actorlayer.Envelope{
		ID:          uuid.NewString(),
		Namespace:   baldaruntime.NamespaceScheduleInbound,
		Kind:        baldaruntime.KindScheduledTask,
		From:        actorlayer.ActorAddress{Target: "schedule", Key: strings.TrimSpace(scheduledTaskID)},
		To:          actorlayer.ActorAddress{Target: baldaruntime.ActorTypeTask, Key: taskID},
		SessionID:   locator.SessionID,
		TaskID:      taskID,
		DedupeKey:   strings.TrimSpace(dispatchKey),
		PayloadJSON: string(data),
	}, nil
}

func shortTaskHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])[:16]
}

func (e *taskActorExecutor) Address() string {
	return actorlayer.WildcardAddress(baldaruntime.ActorTypeTask)
}

func (e *taskActorExecutor) Handle(ctx context.Context, env actorlayer.Envelope) error {
	var payload taskEnvelopePayload
	if err := json.Unmarshal([]byte(env.PayloadJSON), &payload); err != nil {
		return actorlayer.PermanentError(fmt.Errorf("decode task payload: %w", err))
	}
	switch strings.TrimSpace(payload.Kind) {
	case "goal":
		return actorlayer.PolicyError(fmt.Errorf("goal tasks are handled by goal actor"))
	case taskPayloadKindScheduledTask:
		if payload.ScheduledTask == nil {
			return actorlayer.PolicyError(fmt.Errorf("scheduled task payload is required"))
		}
		return e.startScheduledTaskTask(ctx, env, *payload.ScheduledTask)
	case taskPayloadKindWebhookSessionTurn:
		if payload.SessionTurn == nil {
			return actorlayer.PolicyError(fmt.Errorf("session turn task payload is required"))
		}
		if !strings.EqualFold(env.Namespace, baldaruntime.NamespaceWebhookInbound) {
			return actorlayer.PolicyError(fmt.Errorf("session turn tasks are reserved for durable webhook delivery"))
		}
		return e.dispatchWebhookSessionTurn(ctx, env, *payload.SessionTurn)
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported task payload kind %q", payload.Kind))
	}
}

func (e *taskActorExecutor) dispatchWebhookSessionTurn(ctx context.Context, env actorlayer.Envelope, payload SessionTurnPayload) error {
	taskID := firstNonEmpty(env.TaskID, env.To.Key)
	if taskID != "" && e.tasks != nil {
		if _, ok, err := e.tasks.Get(ctx, taskID); err != nil {
			return actorlayer.TransientError(err)
		} else if ok {
			return nil
		}
		created, err := e.tasks.Create(ctx, baldastate.SwarmTaskRecord{
			ID:            taskID,
			SessionID:     strings.TrimSpace(payload.Locator.SessionID),
			ParentTaskID:  strings.TrimSpace(payload.ParentTaskID),
			Title:         webhookTaskTitle(),
			Objective:     strings.TrimSpace(payload.Text),
			Status:        baldastate.SwarmTaskStatusCreated,
			OwnerActor:    baldaruntime.ActorTypeTask + ":" + taskID,
			AssignedActor: baldaruntime.ActorTypeSession + ":" + payload.Locator.SessionID,
			Priority:      webhookTaskPriority(),
			CreatedBy:     strings.TrimSpace(payload.UserID),
		}, "task.actor", payload)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if !created {
			return nil
		}
	}
	payload.TaskID = taskID
	sessionEnv, err := SessionTurnEnvelope(payload)
	if err != nil {
		return actorlayer.PermanentError(err)
	}
	sessionEnv.TaskID = taskID
	sessionEnv.CorrelationID = firstNonEmpty(env.CorrelationID, taskID)
	sessionEnv.CausationID = env.ID
	if strings.TrimSpace(sessionEnv.DedupeKey) != "" {
		sessionEnv.ID = sessionEnv.DedupeKey
	}
	if _, err := e.dispatcher.Dispatch(ctx, sessionEnv); err != nil {
		return actorlayer.TransientError(err)
	}
	if taskID != "" && e.tasks != nil {
		if err := e.tasks.MarkStatus(ctx, taskID, baldastate.SwarmTaskStatusRunning, "task.actor", env.ID, "", nil); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	return nil
}

func webhookTaskTitle() string {
	return "Webhook task"
}

func webhookTaskPriority() int {
	return 80
}

func (e *taskActorExecutor) startScheduledTaskTask(ctx context.Context, env actorlayer.Envelope, payload scheduledTaskPayload) error {
	taskID := firstNonEmpty(env.TaskID, env.To.Key)
	content := strings.TrimSpace(payload.Content)
	if taskID == "" {
		return actorlayer.PolicyError(fmt.Errorf("task id is required"))
	}
	if strings.TrimSpace(payload.TaskID) == "" {
		return actorlayer.PolicyError(fmt.Errorf("scheduled task id is required"))
	}
	if content == "" {
		return actorlayer.PolicyError(fmt.Errorf("scheduled task content is required"))
	}
	if e.tasks != nil {
		if _, ok, err := e.tasks.Get(ctx, taskID); err != nil {
			return actorlayer.TransientError(err)
		} else if ok {
			return nil
		}
		created, err := e.tasks.Create(ctx, baldastate.SwarmTaskRecord{
			ID:            taskID,
			SessionID:     strings.TrimSpace(payload.Locator.SessionID),
			ParentTaskID:  strings.TrimSpace(payload.ParentTaskID),
			Title:         "Scheduled task: " + strings.TrimSpace(payload.TaskID),
			Objective:     content,
			Status:        baldastate.SwarmTaskStatusCreated,
			OwnerActor:    baldaruntime.ActorTypeTask + ":" + taskID,
			AssignedActor: baldaruntime.ActorTypeSession + ":" + payload.Locator.SessionID,
			Priority:      50,
			CreatedBy:     strings.TrimSpace(payload.UserID),
		}, "task.actor", payload)
		if err != nil {
			return actorlayer.TransientError(err)
		}
		if !created {
			return nil
		}
	}
	sessionPayload := SessionTurnPayload{
		Text:            content,
		Locator:         payload.Locator,
		ReportTo:        payload.ReportTo,
		ParentTaskID:    strings.TrimSpace(payload.ParentTaskID),
		UserID:          payload.UserID,
		ScheduledTaskID: payload.TaskID,
		TopicID:         payload.TopicID,
		DeliveryOptions: deliveryfmt.Options{
			Profile: deliveryfmt.Profile{Format: deliveryfmt.FormatAuto},
		},
		Deliver:   payload.ReportTo != nil,
		Source:    sessionTurnSourceSchedule,
		DedupeKey: firstNonEmpty(env.DedupeKey, taskID) + ":session",
	}
	sessionEnv, err := SessionTurnEnvelope(sessionPayload)
	if err != nil {
		return actorlayer.PermanentError(err)
	}
	sessionEnv.TaskID = taskID
	sessionEnv.CorrelationID = firstNonEmpty(env.CorrelationID, taskID)
	sessionEnv.CausationID = env.ID
	if strings.TrimSpace(sessionEnv.DedupeKey) != "" {
		sessionEnv.ID = sessionEnv.DedupeKey
	}
	if _, err := e.dispatcher.Dispatch(ctx, sessionEnv); err != nil {
		return actorlayer.TransientError(err)
	}
	if e.tasks != nil {
		if err := e.tasks.MarkStatus(ctx, taskID, baldastate.SwarmTaskStatusRunning, "task.actor", env.ID, "", nil); err != nil {
			return actorlayer.TransientError(err)
		}
	}
	return nil
}
