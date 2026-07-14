package goalkeeper

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/go-actorlayer"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/goalcmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

func (c *coordinator) askQuestion(ctx context.Context, payload goalJobPayload, prompt string, timeout time.Duration) (baldastate.QuestionRecord, error) {
	if c == nil || c.questions == nil {
		return baldastate.QuestionRecord{}, actorlayer.TransientError(fmt.Errorf("question service is required"))
	}
	if c.dispatcher == nil {
		return baldastate.QuestionRecord{}, actorlayer.TransientError(fmt.Errorf("actor dispatcher is required"))
	}
	jobID := strings.TrimSpace(payload.JobID)
	if jobID == "" {
		return baldastate.QuestionRecord{}, actorlayer.PolicyError(fmt.Errorf("job id is required"))
	}
	resumePayloadJSON, err := goalcmd.EncodeJobPayload(payload)
	if err != nil {
		return baldastate.QuestionRecord{}, actorlayer.PermanentError(fmt.Errorf("encode question resume payload: %w", err))
	}
	interaction := questioncmd.InteractionContext{
		SessionID:   strings.TrimSpace(payload.Locator.SessionID),
		ChannelKind: strings.TrimSpace(payload.Locator.ChannelType),
		Locator:     normalizeGoalDeliveryLocator(payload.Locator),
		RequestedBy: questioncmd.UserRef{
			UserID: strings.TrimSpace(payload.TransportUserID),
		},
		Origin: questioncmd.InteractionOrigin{
			RootJobID: jobID,
		},
		ConversationID: strings.TrimSpace(payload.Locator.AddressKey),
	}
	record, err := c.questions.Ask(ctx, interaction, questioncmd.ResumeTarget{
		To:        baldaexecution.ActorTypeGoalkeeper + ":" + jobID,
		Namespace: baldaexecution.NamespaceGoalkeeperCommand,
		Metadata: map[string]string{
			"goal_payload": resumePayloadJSON,
		},
	}, questioncmd.Request{
		Prompt:        strings.TrimSpace(prompt),
		AllowFreeText: true,
		Timeout:       timeout,
	})
	if err != nil {
		return baldastate.QuestionRecord{}, actorlayer.TransientError(err)
	}
	env, err := deliverycmd.AgentReplyEnvelopeWithProfileAndSettlementAndRefs(
		jobID,
		actorlayer.ActorAddress{Target: baldaexecution.ActorTypeGoalkeeper, Key: jobID},
		normalizeGoalDeliveryLocator(payload.Locator),
		goalDeliveryProfile(payload),
		deliverycmd.SettlementOutbox,
		record.Prompt,
		"question:"+record.QuestionID,
		map[string]string{"question_id": record.QuestionID},
	)
	if err != nil {
		return baldastate.QuestionRecord{}, actorlayer.PermanentError(fmt.Errorf("build question delivery envelope: %w", err))
	}
	if _, err := c.dispatcher.Dispatch(ctx, env); err != nil {
		return baldastate.QuestionRecord{}, actorlayer.TransientError(fmt.Errorf("dispatch question delivery: %w", err))
	}
	if c.jobs != nil {
		if err := c.jobs.MarkStatus(ctx, jobID, baldastate.JobStatusWaitingForUser, actorName, env.ID, "question asked", map[string]any{
			"question_id": record.QuestionID,
		}); err != nil {
			return baldastate.QuestionRecord{}, actorlayer.TransientError(err)
		}
	}
	if c.events != nil {
		if err := c.events.AppendEvent(ctx, jobID, "goal.question.asked", actorName, env.ID, map[string]any{
			"question_id": record.QuestionID,
			"prompt":      record.Prompt,
		}); err != nil {
			return baldastate.QuestionRecord{}, actorlayer.TransientError(err)
		}
	}
	return record, nil
}
