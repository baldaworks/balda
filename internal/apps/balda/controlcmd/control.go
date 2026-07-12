package controlcmd

import (
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/google/uuid"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
)

const (
	ActionCancel       = "cancel"
	ActionCancelTurn   = "cancel_turn"
	ActionClearGoal    = "clear_goal"
	ActionScheduleWait = "schedule_wait"
)

type WaitSchedulePayload struct {
	JobID        string `json:"job_id,omitempty"`
	Content      string `json:"content,omitempty"`
	DelaySeconds int    `json:"delay_seconds,omitempty"`
	RequestedBy  string `json:"requested_by,omitempty"`
	ReportToSelf bool   `json:"report_to_self,omitempty"`
}

type Payload struct {
	Action      string                      `json:"action"`
	JobID       string                      `json:"job_id,omitempty"`
	SessionID   string                      `json:"session_id,omitempty"`
	Locator     baldasession.SessionLocator `json:"locator"`
	Reason      string                      `json:"reason,omitempty"`
	RequestedBy string                      `json:"requested_by,omitempty"`
	Notify      bool                        `json:"notify,omitempty"`
	Wait        *WaitSchedulePayload        `json:"wait,omitempty"`
}

func CancelEnvelopeWithNotify(locator baldasession.SessionLocator, jobID string, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return envelope(locator, ActionCancel, jobID, requestedBy, reason, notify, nil)
}

func CancelTurnEnvelopeWithNotify(locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return envelope(locator, ActionCancelTurn, "", requestedBy, reason, notify, nil)
}

func ClearGoalEnvelopeWithNotify(locator baldasession.SessionLocator, requestedBy string, reason string, notify bool) (actorlayer.Envelope, error) {
	return envelope(locator, ActionClearGoal, "", requestedBy, reason, notify, nil)
}

func ScheduleWaitEnvelope(locator baldasession.SessionLocator, jobID string, content string, delaySeconds int, requestedBy string, notify bool) (actorlayer.Envelope, error) {
	return envelope(locator, ActionScheduleWait, "", requestedBy, "", notify, &WaitSchedulePayload{
		JobID:        strings.TrimSpace(jobID),
		Content:      strings.TrimSpace(content),
		DelaySeconds: delaySeconds,
		RequestedBy:  strings.TrimSpace(requestedBy),
		ReportToSelf: true,
	})
}

func envelope(locator baldasession.SessionLocator, action string, jobID string, requestedBy string, reason string, notify bool, wait *WaitSchedulePayload) (actorlayer.Envelope, error) {
	payload := Payload{
		Action:      strings.TrimSpace(action),
		JobID:       strings.TrimSpace(jobID),
		SessionID:   strings.TrimSpace(locator.SessionID),
		Locator:     locator,
		Reason:      strings.TrimSpace(reason),
		RequestedBy: strings.TrimSpace(requestedBy),
		Notify:      notify,
		Wait:        wait,
	}
	data, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode control payload: %w", err)
	}
	id := uuid.NewString()
	return actorlayer.Envelope{
		ID:        id,
		Namespace: baldaexecution.NamespaceJobControl,
		Kind:      baldaexecution.KindCancel,
		From:      actorlayer.ActorAddress{Target: "telegram", Key: firstNonEmpty(requestedBy, locator.AddressKey, "unknown")},
		To:        actorlayer.SystemAddress("control"),
		Meta:      baldaexecution.WithSessionIDMeta(baldaexecution.WithJobIDMeta(nil, jobID), locator.SessionID),
		Priority:  100,
		DedupeKey: "control:" + strings.TrimSpace(action) + ":" + id,
		Payload:   data,
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}
