package deliverycmd

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldaruntime "github.com/normahq/balda/internal/apps/balda/runtime"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer"
)

const taskPayloadKindDelivery = "delivery"

type Payload struct {
	TaskID     string                      `json:"task_id,omitempty"`
	Locator    baldasession.SessionLocator `json:"locator"`
	Profile    Profile                     `json:"profile,omitempty,omitzero"`
	Mode       Mode                        `json:"mode"`
	Settlement SettlementPolicy            `json:"settlement,omitempty"`
	Text       string                      `json:"text,omitempty"`
	DraftID    int                         `json:"draft_id,omitempty"`
	Action     string                      `json:"action,omitempty"`
	Progress   *Progress                   `json:"progress,omitempty"`
}

type Mode string

type SettlementPolicy string

type ProgressKind string

type Profile = deliveryfmt.Profile

type Progress struct {
	Kind     ProgressKind               `json:"kind"`
	Text     string                     `json:"text,omitempty"`
	Plan     *progress.PlanSnapshot     `json:"plan,omitempty"`
	Visible  bool                       `json:"visible,omitempty"`
	Policy   deliveryfmt.ProgressPolicy `json:"policy,omitempty,omitzero"`
	DraftID  int                        `json:"draft_id,omitempty"`
	Sequence int                        `json:"sequence,omitempty"`
}

const (
	ModeAgentReply Mode = "agent_reply"
	ModePlain      Mode = "plain"
	ModeMarkdown   Mode = "markdown"
	ModeDraftPlain Mode = "draft_plain"
	ModeChatAction Mode = "chat_action"
	ModeProgress   Mode = "progress"
)

const (
	SettlementAuto   SettlementPolicy = "auto"
	SettlementBypass SettlementPolicy = "bypass"
	SettlementOutbox SettlementPolicy = "outbox"
)

const (
	ProgressActivity   ProgressKind = "activity"
	ProgressThinking   ProgressKind = "thinking"
	ProgressPlanUpdate ProgressKind = "plan_update"
)

func AgentReplyEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithProfile(taskID, from, locator, Profile{}, text, dedupeSuffix)
}

func AgentReplyEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithProfileAndSettlement(taskID, from, locator, Profile{}, settlement, text, dedupeSuffix)
}

func AgentReplyEnvelopeWithProfile(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile Profile, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithProfileAndSettlement(taskID, from, locator, profile, SettlementAuto, text, dedupeSuffix)
}

func AgentReplyEnvelopeWithProfileAndSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile Profile, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:     strings.TrimSpace(taskID),
		Locator:    locator,
		Profile:    normalizeProfile(profile),
		Mode:       ModeAgentReply,
		Settlement: normalizeSettlementPolicy(settlement),
		Text:       strings.TrimSpace(text),
	}, dedupeSuffix)
}

func PlainEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return PlainEnvelopeWithSettlement(taskID, from, locator, SettlementAuto, text, dedupeSuffix)
}

func PlainEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:     strings.TrimSpace(taskID),
		Locator:    locator,
		Mode:       ModePlain,
		Settlement: normalizeSettlementPolicy(settlement),
		Text:       strings.TrimSpace(text),
	}, dedupeSuffix)
}

func MarkdownEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return MarkdownEnvelopeWithProfile(taskID, from, locator, Profile{}, text, dedupeSuffix)
}

func MarkdownEnvelopeWithSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return MarkdownEnvelopeWithProfileAndSettlement(taskID, from, locator, Profile{}, settlement, text, dedupeSuffix)
}

func MarkdownEnvelopeWithProfile(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile Profile, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return MarkdownEnvelopeWithProfileAndSettlement(taskID, from, locator, profile, SettlementAuto, text, dedupeSuffix)
}

func MarkdownEnvelopeWithProfileAndSettlement(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, profile Profile, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:     strings.TrimSpace(taskID),
		Locator:    locator,
		Profile:    normalizeProfile(profile),
		Mode:       ModeMarkdown,
		Settlement: normalizeSettlementPolicy(settlement),
		Text:       strings.TrimSpace(text),
	}, dedupeSuffix)
}

func DraftPlainEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, draftID int, text string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    ModeDraftPlain,
		Text:    strings.TrimSpace(text),
		DraftID: draftID,
	}, "")
}

func ChatActionEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, action string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    ModeChatAction,
		Action:  strings.TrimSpace(action),
	}, "")
}

func ProgressActivityEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    ModeProgress,
		Progress: &Progress{
			Kind:     ProgressActivity,
			Visible:  false,
			Policy:   policy,
			Sequence: sequence,
		},
	}, dedupeSuffix)
}

func ProgressThinkingEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, draftID int, text string, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    ModeProgress,
		Progress: &Progress{
			Kind:     ProgressThinking,
			Text:     strings.TrimSpace(text),
			Visible:  visible,
			Policy:   policy,
			DraftID:  draftID,
			Sequence: sequence,
		},
	}, dedupeSuffix)
}

func ProgressPlanUpdateEnvelope(taskID string, from actorlayer.ActorAddress, locator baldasession.SessionLocator, policy deliveryfmt.ProgressPolicy, visible bool, draftID int, plan *progress.PlanSnapshot, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(taskID, from, Payload{
		TaskID:  strings.TrimSpace(taskID),
		Locator: locator,
		Mode:    ModeProgress,
		Progress: &Progress{
			Kind:    ProgressPlanUpdate,
			Text:    strings.TrimSpace(text),
			Plan:    plan,
			Visible: visible,
			Policy:  policy,
			DraftID: draftID,
		},
	}, dedupeSuffix)
}

func normalizeProfile(profile Profile) Profile { return deliveryfmt.NormalizeProfile(profile) }

func normalizeSettlementPolicy(policy SettlementPolicy) SettlementPolicy {
	switch strings.TrimSpace(string(policy)) {
	case "", string(SettlementAuto):
		return SettlementAuto
	case string(SettlementBypass):
		return SettlementBypass
	case string(SettlementOutbox):
		return SettlementOutbox
	default:
		return SettlementPolicy(strings.TrimSpace(string(policy)))
	}
}

func Validate(payload Payload) error {
	switch payload.Mode {
	case ModeAgentReply, ModePlain, ModeMarkdown, ModeDraftPlain:
		if strings.TrimSpace(payload.Text) == "" {
			return fmt.Errorf("delivery text is required")
		}
	case ModeChatAction:
		if strings.TrimSpace(payload.Action) == "" {
			return fmt.Errorf("delivery action is required")
		}
	case ModeProgress:
		if payload.Progress == nil {
			return fmt.Errorf("delivery progress is required")
		}
		switch payload.Progress.Kind {
		case ProgressActivity:
			return nil
		case ProgressThinking:
			if payload.Progress.Visible && payload.Progress.DraftID <= 0 && payload.Progress.Policy.Thinking {
				return fmt.Errorf("thinking progress draft id is required when draft placeholders are enabled")
			}
		case ProgressPlanUpdate:
			if strings.TrimSpace(payload.Progress.Text) == "" && (payload.Progress.Plan == nil || len(payload.Progress.Plan.Entries) == 0) {
				return fmt.Errorf("plan update progress text or plan snapshot is required")
			}
		default:
			return fmt.Errorf("unsupported progress kind %q", payload.Progress.Kind)
		}
	default:
		return fmt.Errorf("unsupported delivery mode %q", payload.Mode)
	}
	if payload.Mode == ModeDraftPlain && payload.DraftID <= 0 {
		return fmt.Errorf("draft id is required")
	}
	switch normalizeSettlementPolicy(payload.Settlement) {
	case SettlementAuto, SettlementBypass, SettlementOutbox:
	default:
		return fmt.Errorf("unsupported delivery settlement %q", payload.Settlement)
	}
	return nil
}

func envelope(taskID string, from actorlayer.ActorAddress, payload Payload, dedupeSuffix string) (actorlayer.Envelope, error) {
	if strings.TrimSpace(payload.Locator.ChannelType) == "" || strings.TrimSpace(payload.Locator.AddressKey) == "" || strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.Envelope{}, fmt.Errorf("delivery locator is required")
	}
	if err := Validate(payload); err != nil {
		return actorlayer.Envelope{}, err
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode delivery payload: %w", err)
	}
	dedupeKey := deliveryDedupeKey(taskID, payload.Mode, dedupeSuffix)
	return actorlayer.Envelope{
		ID:            dedupeKey,
		Namespace:     baldaruntime.NamespaceAgentResult,
		Kind:          taskPayloadKindDelivery,
		From:          from,
		To:            actorlayer.ActorAddress{Target: baldaruntime.ActorTypeDelivery, Key: payload.Locator.DeliveryActorKey()},
		SessionID:     payload.Locator.SessionID,
		TaskID:        strings.TrimSpace(taskID),
		CorrelationID: strings.TrimSpace(taskID),
		Priority:      70,
		DedupeKey:     dedupeKey,
		PayloadJSON:   string(data),
	}, nil
}

func deliveryDedupeKey(taskID string, mode Mode, dedupeSuffix string) string {
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
