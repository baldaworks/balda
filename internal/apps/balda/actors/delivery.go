package actors

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/swarm"
	"github.com/normahq/balda/pkg/actorlayer"
)

func DeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	text string,
	dedupeSuffix string,
) (actorlayer.Envelope, error) {
	return AgentReplyDeliveryEnvelope(taskID, from, locator, text, dedupeSuffix)
}

func AgentReplyDeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	text string,
	dedupeSuffix string,
) (actorlayer.Envelope, error) {
	return deliveryEnvelope(taskID, from, DeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    DeliveryModeAgentReply,
		Text:    strings.TrimSpace(text),
	}, dedupeSuffix)
}

func PlainDeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	text string,
	dedupeSuffix string,
) (actorlayer.Envelope, error) {
	return deliveryEnvelope(taskID, from, DeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    DeliveryModePlain,
		Text:    strings.TrimSpace(text),
	}, dedupeSuffix)
}

func MarkdownDeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	text string,
	dedupeSuffix string,
) (actorlayer.Envelope, error) {
	return deliveryEnvelope(taskID, from, DeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    DeliveryModeMarkdown,
		Text:    strings.TrimSpace(text),
	}, dedupeSuffix)
}

func DraftPlainDeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	draftID int,
	text string,
) (actorlayer.Envelope, error) {
	return deliveryEnvelope(taskID, from, DeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    DeliveryModeDraftPlain,
		Text:    strings.TrimSpace(text),
		DraftID: draftID,
	}, "")
}

func ChatActionDeliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	action string,
) (actorlayer.Envelope, error) {
	return deliveryEnvelope(taskID, from, DeliveryPayload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    DeliveryModeChatAction,
		Action:  strings.TrimSpace(action),
	}, "")
}

func deliveryEnvelope(
	taskID string,
	from actorlayer.ActorAddress,
	payload DeliveryPayload,
	dedupeSuffix string,
) (actorlayer.Envelope, error) {
	if strings.TrimSpace(payload.Locator.ChannelType) == "" || strings.TrimSpace(payload.Locator.AddressKey) == "" || strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.Envelope{}, fmt.Errorf("delivery locator is required")
	}
	if err := validateDeliveryPayload(payload); err != nil {
		return actorlayer.Envelope{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode delivery payload: %w", err)
	}

	dedupeKey := deliveryDedupeKey(taskID, payload.Mode, dedupeSuffix)
	return actorlayer.Envelope{
		ID:            dedupeKey,
		Namespace:     swarm.NamespaceAgentResult,
		Kind:          taskPayloadKindDelivery,
		From:          from,
		To:            actorlayer.ActorAddress{Target: swarm.ActorTypeDelivery, Key: payload.Locator.DeliveryActorKey()},
		SessionID:     payload.Locator.SessionID,
		TaskID:        strings.TrimSpace(taskID),
		CorrelationID: strings.TrimSpace(taskID),
		Priority:      70,
		DedupeKey:     dedupeKey,
		PayloadJSON:   string(data),
	}, nil
}

func validateDeliveryPayload(payload DeliveryPayload) error {
	switch payload.Mode {
	case DeliveryModeAgentReply, DeliveryModePlain, DeliveryModeMarkdown, DeliveryModeDraftPlain:
		if strings.TrimSpace(payload.Text) == "" {
			return fmt.Errorf("delivery text is required")
		}
	case DeliveryModeChatAction:
		if strings.TrimSpace(payload.Action) == "" {
			return fmt.Errorf("delivery action is required")
		}
	default:
		return fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
	if payload.Mode == DeliveryModeDraftPlain && payload.DraftID <= 0 {
		return fmt.Errorf("draft id is required")
	}
	return nil
}

func deliveryDedupeKey(taskID string, mode DeliveryMode, dedupeSuffix string) string {
	trimmedTaskID := strings.TrimSpace(taskID)
	if trimmedTaskID == "" {
		id := "delivery:" + strings.ToLower(string(mode)) + ":" + uuid.NewString()
		if suffix := strings.TrimSpace(dedupeSuffix); suffix != "" {
			return id + ":" + suffix
		}
		return id
	}
	if suffix := strings.TrimSpace(dedupeSuffix); suffix != "" {
		return trimmedTaskID + ":delivery:" + suffix
	}
	return trimmedTaskID + ":delivery:" + strings.ToLower(string(mode)) + ":" + uuid.NewString()
}
