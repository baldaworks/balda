package deliverycmd

import (
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/google/uuid"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
)

const jobPayloadKindDelivery = "delivery"

type Payload struct {
	JobID          string                     `json:"job_id,omitempty"`
	Locator        Locator                    `json:"locator"`
	DeliveryFormat deliveryfmt.DeliveryFormat `json:"delivery_format,omitempty"`
	Mode           Mode                       `json:"mode"`
	Settlement     SettlementPolicy           `json:"settlement,omitempty"`
	Refs           map[string]string          `json:"refs,omitempty"`
	Question       *Question                  `json:"question,omitempty"`
	Text           string                     `json:"text,omitempty"`
	DraftID        int                        `json:"draft_id,omitempty"`
	Action         string                     `json:"action,omitempty"`
	MessageID      string                     `json:"message_id,omitempty"`
	Handle         string                     `json:"handle,omitempty"`
	Progress       *Progress                  `json:"progress,omitempty"`
	Media          *Media                     `json:"media,omitempty"`
}

type Mode string

type SettlementPolicy string

type ProgressKind string

type ProgressPolicy struct {
	Typing      bool `json:"typing,omitempty"`
	Thinking    bool `json:"thinking,omitempty"`
	PlanUpdates bool `json:"plan_updates,omitempty"`
}

type PlanSnapshot struct {
	Entries []PlanEntry `json:"entries,omitempty"`
}

type PlanEntry struct {
	Content string `json:"content"`
	Status  string `json:"status,omitempty"`
}

// Question describes transport-neutral choices attached to a delivered prompt.
type Question struct {
	ID       string           `json:"id"`
	Options  []QuestionOption `json:"options"`
	Audience QuestionAudience `json:"audience,omitempty,omitzero"`
}

// QuestionAudience describes who may see a delivered question without
// prescribing how a concrete channel enforces that visibility.
type QuestionAudience struct {
	Visibility QuestionVisibility `json:"visibility,omitempty"`
	UserID     string             `json:"user_id,omitempty"`
}

type QuestionVisibility string

const QuestionVisibilityPrivate QuestionVisibility = "private"

// QuestionOption is one selectable value in a delivered question.
type QuestionOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Progress struct {
	Kind     ProgressKind   `json:"kind"`
	Text     string         `json:"text,omitempty"`
	Plan     *PlanSnapshot  `json:"plan,omitempty"`
	Visible  bool           `json:"visible,omitempty"`
	Policy   ProgressPolicy `json:"policy,omitempty,omitzero"`
	Sequence int            `json:"sequence,omitempty"`
}

const (
	ModeAgentReply            Mode = "agent_reply"
	ModePlain                 Mode = "plain"
	ModeMarkdown              Mode = "markdown"
	ModeDraftPlain            Mode = "draft_plain"
	ModeChatAction            Mode = "chat_action"
	ModeProgress              Mode = "progress"
	ModeClearQuestionControls Mode = "clear_question_controls"
	ModePhoto                 Mode = "photo"
	ModeDocument              Mode = "document"
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

func AgentReplyEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithFormat(jobID, from, locator, "", text, dedupeSuffix)
}

func AgentReplyEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithFormatAndSettlement(jobID, from, locator, "", settlement, text, dedupeSuffix)
}

func AgentReplyEnvelopeWithFormat(jobID string, from actorlayer.ActorAddress, locator Locator, format deliveryfmt.DeliveryFormat, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithFormatAndSettlement(jobID, from, locator, format, SettlementAuto, text, dedupeSuffix)
}

func AgentReplyEnvelopeWithFormatAndSettlement(jobID string, from actorlayer.ActorAddress, locator Locator, format deliveryfmt.DeliveryFormat, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return AgentReplyEnvelopeWithFormatAndSettlementAndRefs(jobID, from, locator, format, settlement, text, dedupeSuffix, nil)
}

func AgentReplyEnvelopeWithFormatAndSettlementAndRefs(jobID string, from actorlayer.ActorAddress, locator Locator, format deliveryfmt.DeliveryFormat, settlement SettlementPolicy, text string, dedupeSuffix string, refs map[string]string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:          strings.TrimSpace(jobID),
		Locator:        locator,
		DeliveryFormat: deliveryfmt.NormalizeDeliveryFormat(format),
		Mode:           ModeAgentReply,
		Settlement:     normalizeSettlementPolicy(settlement),
		Refs:           refs,
		Text:           strings.TrimSpace(text),
	}, dedupeSuffix)
}

// QuestionEnvelope builds an agent-reply delivery carrying generic selectable options.
func QuestionEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, format deliveryfmt.DeliveryFormat, settlement SettlementPolicy, text, questionID, dedupeSuffix string, options []QuestionOption, audience QuestionAudience) (actorlayer.Envelope, error) {
	questionID = strings.TrimSpace(questionID)
	return envelope(jobID, from, Payload{
		JobID:          strings.TrimSpace(jobID),
		Locator:        locator,
		DeliveryFormat: deliveryfmt.NormalizeDeliveryFormat(format),
		Mode:           ModeAgentReply,
		Settlement:     normalizeSettlementPolicy(settlement),
		Refs:           map[string]string{"question_id": questionID},
		Question: &Question{
			ID:       questionID,
			Options:  append([]QuestionOption(nil), options...),
			Audience: audience,
		},
		Text: strings.TrimSpace(text),
	}, dedupeSuffix)
}

// ClearQuestionControlsEnvelope removes channel-native controls from a settled question.
func ClearQuestionControlsEnvelope(from actorlayer.ActorAddress, locator Locator, questionID, messageID, handle string) (actorlayer.Envelope, error) {
	return SettleQuestionControlsEnvelope(from, locator, questionID, messageID, handle, "")
}

// SettleQuestionControlsEnvelope removes channel-native controls and carries
// an optional structured-selection acknowledgement for channel projection.
func SettleQuestionControlsEnvelope(from actorlayer.ActorAddress, locator Locator, questionID, messageID, handle, selectionText string) (actorlayer.Envelope, error) {
	questionID = strings.TrimSpace(questionID)
	env, err := envelope("", from, Payload{
		Locator:   locator,
		Mode:      ModeClearQuestionControls,
		Refs:      map[string]string{"question_id": questionID},
		MessageID: strings.TrimSpace(messageID),
		Handle:    strings.TrimSpace(handle),
		Text:      strings.TrimSpace(selectionText),
	}, "question-controls-clear")
	if err != nil {
		return actorlayer.Envelope{}, err
	}
	dedupeKey := "question:" + questionID + ":controls:clear"
	env.ID = dedupeKey
	env.DedupeKey = dedupeKey
	return env, nil
}

func PlainEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return PlainEnvelopeWithSettlement(jobID, from, locator, SettlementAuto, text, dedupeSuffix)
}

func PlainEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:      strings.TrimSpace(jobID),
		Locator:    locator,
		Mode:       ModePlain,
		Settlement: normalizeSettlementPolicy(settlement),
		Text:       strings.TrimSpace(text),
	}, dedupeSuffix)
}

func MarkdownEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return MarkdownEnvelopeWithSettlement(jobID, from, locator, SettlementAuto, text, dedupeSuffix)
}

func MarkdownEnvelopeWithSettlement(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:      strings.TrimSpace(jobID),
		Locator:    locator,
		Mode:       ModeMarkdown,
		Settlement: normalizeSettlementPolicy(settlement),
		Text:       strings.TrimSpace(text),
	}, dedupeSuffix)
}

func DraftPlainEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, draftID int, text string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:   strings.TrimSpace(jobID),
		Locator: locator,
		Mode:    ModeDraftPlain,
		Text:    strings.TrimSpace(text),
		DraftID: draftID,
	}, "")
}

func ChatActionEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, action string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:   strings.TrimSpace(jobID),
		Locator: locator,
		Mode:    ModeChatAction,
		Action:  strings.TrimSpace(action),
	}, "")
}

func PhotoEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, fileID, caption, dedupeSuffix string) (actorlayer.Envelope, error) {
	return PhotoLocalEnvelope(jobID, from, locator, settlement, "", fileID, caption, dedupeSuffix)
}

func PhotoLocalEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, localPath, fileID, caption, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:      strings.TrimSpace(jobID),
		Locator:    locator,
		Mode:       ModePhoto,
		Settlement: normalizeSettlementPolicy(settlement),
		Media: &Media{
			Kind:       "photo",
			FileID:     strings.TrimSpace(fileID),
			LocalPath:  strings.TrimSpace(localPath),
			Caption:    strings.TrimSpace(caption),
			SourceKind: mediaSourceKind(localPath, fileID),
		},
	}, dedupeSuffix)
}

func DocumentEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, fileID, caption, name, dedupeSuffix string) (actorlayer.Envelope, error) {
	return DocumentLocalEnvelope(jobID, from, locator, settlement, "", fileID, caption, name, "", dedupeSuffix)
}

func DocumentLocalEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, settlement SettlementPolicy, localPath, fileID, caption, name, mimeType, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:      strings.TrimSpace(jobID),
		Locator:    locator,
		Mode:       ModeDocument,
		Settlement: normalizeSettlementPolicy(settlement),
		Media: &Media{
			Kind:       "document",
			FileID:     strings.TrimSpace(fileID),
			LocalPath:  strings.TrimSpace(localPath),
			Caption:    strings.TrimSpace(caption),
			Name:       strings.TrimSpace(name),
			MIMEType:   strings.TrimSpace(mimeType),
			SourceKind: mediaSourceKind(localPath, fileID),
		},
	}, dedupeSuffix)
}

func mediaSourceKind(localPath, fileID string) string {
	if strings.TrimSpace(localPath) != "" {
		return "local"
	}
	if strings.TrimSpace(fileID) != "" {
		return "file_id"
	}
	return ""
}

func ProgressActivityEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, policy ProgressPolicy, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:   strings.TrimSpace(jobID),
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

func ProgressThinkingEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, policy ProgressPolicy, visible bool, text string, sequence int, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:   strings.TrimSpace(jobID),
		Locator: locator,
		Mode:    ModeProgress,
		Progress: &Progress{
			Kind:     ProgressThinking,
			Text:     strings.TrimSpace(text),
			Visible:  visible,
			Policy:   policy,
			Sequence: sequence,
		},
	}, dedupeSuffix)
}

func ProgressPlanUpdateEnvelope(jobID string, from actorlayer.ActorAddress, locator Locator, policy ProgressPolicy, visible bool, plan *PlanSnapshot, text string, dedupeSuffix string) (actorlayer.Envelope, error) {
	return envelope(jobID, from, Payload{
		JobID:   strings.TrimSpace(jobID),
		Locator: locator,
		Mode:    ModeProgress,
		Progress: &Progress{
			Kind:    ProgressPlanUpdate,
			Text:    strings.TrimSpace(text),
			Plan:    plan,
			Visible: visible,
			Policy:  policy,
		},
	}, dedupeSuffix)
}

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
		if payload.Question != nil {
			if payload.Mode != ModeAgentReply {
				return fmt.Errorf("question controls require agent reply mode")
			}
			if err := validateQuestion(*payload.Question); err != nil {
				return err
			}
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
		case ProgressPlanUpdate:
			if strings.TrimSpace(payload.Progress.Text) == "" && (payload.Progress.Plan == nil || len(payload.Progress.Plan.Entries) == 0) {
				return fmt.Errorf("plan update progress text or plan snapshot is required")
			}
		default:
			return fmt.Errorf("unsupported progress kind %q", payload.Progress.Kind)
		}
	case ModeClearQuestionControls:
		if strings.TrimSpace(payload.Refs["question_id"]) == "" {
			return fmt.Errorf("question id is required")
		}
		if strings.TrimSpace(payload.MessageID) == "" {
			return fmt.Errorf("provider message id is required")
		}
	case ModePhoto, ModeDocument:
		if payload.Media == nil {
			return fmt.Errorf("delivery media is required")
		}
		if strings.TrimSpace(payload.Media.FileID) == "" && strings.TrimSpace(payload.Media.LocalPath) == "" {
			return fmt.Errorf("delivery media file id or local path is required")
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

func validateQuestion(question Question) error {
	if strings.TrimSpace(question.ID) == "" {
		return fmt.Errorf("question id is required")
	}
	if len(question.Options) == 0 {
		return fmt.Errorf("question options are required")
	}
	switch question.Audience.Visibility {
	case "":
	case QuestionVisibilityPrivate:
		if strings.TrimSpace(question.Audience.UserID) == "" {
			return fmt.Errorf("private question audience user id is required")
		}
	default:
		return fmt.Errorf("unsupported question visibility %q", question.Audience.Visibility)
	}
	seen := make(map[string]struct{}, len(question.Options))
	for _, option := range question.Options {
		id := strings.TrimSpace(option.ID)
		if id == "" {
			return fmt.Errorf("question option id is required")
		}
		if strings.TrimSpace(option.Label) == "" {
			return fmt.Errorf("question option label is required")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("duplicate question option id %q", id)
		}
		seen[id] = struct{}{}
	}
	return nil
}

func envelope(jobID string, from actorlayer.ActorAddress, payload Payload, dedupeSuffix string) (actorlayer.Envelope, error) {
	if strings.TrimSpace(payload.Locator.ChannelType) == "" || strings.TrimSpace(payload.Locator.AddressKey) == "" || strings.TrimSpace(payload.Locator.SessionID) == "" {
		return actorlayer.Envelope{}, fmt.Errorf("delivery locator is required")
	}
	trimmedJobID := strings.TrimSpace(jobID)
	trimmedPayloadJobID := strings.TrimSpace(payload.JobID)
	switch {
	case trimmedJobID == "":
		payload.JobID = ""
	case trimmedPayloadJobID == "":
		payload.JobID = trimmedJobID
	case trimmedPayloadJobID != trimmedJobID:
		return actorlayer.Envelope{}, fmt.Errorf("delivery payload job id %q does not match job id %q", trimmedPayloadJobID, trimmedJobID)
	}
	if err := Validate(payload); err != nil {
		return actorlayer.Envelope{}, err
	}
	data, err := actorlayer.MarshalPayload(payload)
	if err != nil {
		return actorlayer.Envelope{}, fmt.Errorf("encode delivery payload: %w", err)
	}
	dedupeKey := deliveryDedupeKey(trimmedJobID, payload.Mode, dedupeSuffix)
	return actorlayer.Envelope{
		ID:            dedupeKey,
		Namespace:     namespaceAgentResult,
		Kind:          jobPayloadKindDelivery,
		From:          from,
		To:            actorlayer.ActorAddress{Target: actorTypeDelivery, Key: payload.Locator.DeliveryActorKey()},
		Meta:          withSessionIDMeta(withJobIDMeta(nil, trimmedJobID), payload.Locator.SessionID),
		CorrelationID: trimmedJobID,
		Priority:      70,
		DedupeKey:     dedupeKey,
		Payload:       data,
	}, nil
}

func deliveryDedupeKey(jobID string, mode Mode, dedupeSuffix string) string {
	trimmedJobID := strings.TrimSpace(jobID)
	if trimmedJobID == "" {
		id := "delivery:" + strings.ToLower(string(mode)) + ":" + uuid.NewString()
		if suffix := strings.TrimSpace(dedupeSuffix); suffix != "" {
			return id + ":" + suffix
		}
		return id
	}
	if suffix := strings.TrimSpace(dedupeSuffix); suffix != "" {
		return trimmedJobID + ":delivery:" + suffix
	}
	return trimmedJobID + ":delivery:" + strings.ToLower(string(mode)) + ":" + uuid.NewString()
}

const (
	namespaceAgentResult = "agent.result"
	actorTypeDelivery    = "delivery"
	jobIDMetaKey         = "job_id"
)

func withJobIDMeta(meta map[string]string, jobID string) map[string]string {
	trimmed := strings.TrimSpace(jobID)
	if trimmed == "" {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	for key, value := range meta {
		out[key] = value
	}
	out[jobIDMetaKey] = trimmed
	return out
}

func withSessionIDMeta(meta map[string]string, sessionID string) map[string]string {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return meta
	}
	out := make(map[string]string, len(meta)+1)
	for key, value := range meta {
		out[key] = value
	}
	out["session_id"] = trimmed
	return out
}
