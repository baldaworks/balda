package questions

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

type Store interface {
	CreatePendingQuestion(ctx context.Context, record baldastate.QuestionRecord) error
	BindQuestionDeliveryRef(ctx context.Context, questionID string, ref questioncmd.DeliveryRef) error
	GetQuestionByID(ctx context.Context, questionID string) (baldastate.QuestionRecord, bool, error)
	GetPendingQuestionByReplyRef(ctx context.Context, provider, conversationKey, replyToMessageID string) (baldastate.QuestionRecord, bool, error)
	MarkQuestionAnswered(ctx context.Context, questionID string, answer questioncmd.Answer) (baldastate.QuestionRecord, bool, error)
	MarkQuestionTimedOut(ctx context.Context, questionID string, timedOutAt time.Time) (baldastate.QuestionRecord, bool, error)
}

type ScheduledJobStore interface {
	Upsert(ctx context.Context, record baldastate.ScheduledJobRecord) error
}

type Service struct {
	store     Store
	scheduled ScheduledJobStore
	logger    zerolog.Logger
	now       func() time.Time
}

func New(store Store, scheduled ScheduledJobStore, logger zerolog.Logger) *Service {
	return &Service{
		store:     store,
		scheduled: scheduled,
		logger:    logger,
		now:       time.Now,
	}
}

func (s *Service) Ask(ctx context.Context, interaction questioncmd.InteractionContext, resume questioncmd.ResumeTarget, req questioncmd.Request) (baldastate.QuestionRecord, error) {
	if s.store == nil {
		return baldastate.QuestionRecord{}, fmt.Errorf("question store is required")
	}
	if strings.TrimSpace(interaction.SessionID) == "" {
		return baldastate.QuestionRecord{}, fmt.Errorf("interaction session_id is required")
	}
	if strings.TrimSpace(interaction.Locator.SessionID) == "" {
		return baldastate.QuestionRecord{}, fmt.Errorf("interaction locator session_id is required")
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return baldastate.QuestionRecord{}, fmt.Errorf("question prompt is required")
	}
	if strings.TrimSpace(resume.To) == "" {
		return baldastate.QuestionRecord{}, fmt.Errorf("resume target is required")
	}
	questionID := "question-" + uuid.NewString()
	now := s.now().UTC()
	record := baldastate.QuestionRecord{
		QuestionID:      questionID,
		SessionID:       strings.TrimSpace(interaction.SessionID),
		ChannelKind:     firstNonEmpty(interaction.ChannelKind, interaction.Locator.ChannelType),
		AddressKey:      strings.TrimSpace(interaction.Locator.AddressKey),
		AddressJSON:     strings.TrimSpace(interaction.Locator.AddressJSON),
		Prompt:          strings.TrimSpace(req.Prompt),
		Status:          questioncmd.StatusPending,
		InteractionJSON: mustJSON(interaction),
		ResumeJSON:      mustJSON(resume),
		RequestJSON:     mustJSON(req),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if req.Timeout > 0 {
		expiresAt := now.Add(req.Timeout)
		record.ExpiresAt = expiresAt
	}
	if err := s.store.CreatePendingQuestion(ctx, record); err != nil {
		return baldastate.QuestionRecord{}, err
	}
	if !record.ExpiresAt.IsZero() && s.scheduled != nil {
		content, err := questioncmd.TimeoutScheduledContent(questionID)
		if err != nil {
			return baldastate.QuestionRecord{}, err
		}
		if err := s.scheduled.Upsert(ctx, baldastate.ScheduledJobRecord{
			JobID:        "question-timeout-" + questionID,
			SessionID:    strings.TrimSpace(interaction.Locator.SessionID),
			ChannelType:  strings.TrimSpace(interaction.Locator.ChannelType),
			AddressKey:   strings.TrimSpace(interaction.Locator.AddressKey),
			AddressJSON:  strings.TrimSpace(interaction.Locator.AddressJSON),
			Content:      content,
			ScheduleSpec: "@once",
			Timezone:     "UTC",
			Status:       baldastate.ScheduledJobStatusActive,
			MaxRetries:   0,
			NextRunAt:    record.ExpiresAt.UTC(),
		}); err != nil {
			return baldastate.QuestionRecord{}, err
		}
	}
	return record, nil
}

func (s *Service) BindDelivery(ctx context.Context, questionID string, ref questioncmd.DeliveryRef) error {
	if s.store == nil {
		return fmt.Errorf("question store is required")
	}
	if strings.TrimSpace(questionID) == "" {
		return fmt.Errorf("question id is required")
	}
	return s.store.BindQuestionDeliveryRef(ctx, questionID, ref)
}

func (s *Service) ResolveReply(ctx context.Context, in questioncmd.InboundReply) (baldastate.QuestionRecord, bool, error) {
	result, err := s.ResolveReplyDetailed(ctx, in)
	return result.Record, result.Matched, err
}

type ReplyResolution struct {
	Record  baldastate.QuestionRecord
	Matched bool
	Settled bool
	Invalid bool
}

func (s *Service) ResolveReplyDetailed(ctx context.Context, in questioncmd.InboundReply) (ReplyResolution, error) {
	if s.store == nil {
		return ReplyResolution{}, fmt.Errorf("question store is required")
	}
	record, ok, err := s.store.GetPendingQuestionByReplyRef(ctx, strings.TrimSpace(in.Provider), strings.TrimSpace(in.ConversationKey), strings.TrimSpace(in.ReplyToMessageID))
	if err != nil || !ok {
		return ReplyResolution{Record: record, Matched: ok}, err
	}
	var request questioncmd.Request
	if strings.TrimSpace(record.RequestJSON) == "" {
		request.AllowFreeText = true
	} else if err := json.Unmarshal([]byte(record.RequestJSON), &request); err != nil {
		return ReplyResolution{Record: record, Matched: true}, fmt.Errorf("decode question request: %w", err)
	}
	if strings.EqualFold(strings.TrimSpace(request.Responder), questioncmd.ResponderRequester) {
		var interaction questioncmd.InteractionContext
		if err := json.Unmarshal([]byte(record.InteractionJSON), &interaction); err != nil {
			return ReplyResolution{Record: record, Matched: true}, fmt.Errorf("decode question interaction: %w", err)
		}
		if strings.TrimSpace(interaction.RequestedBy.UserID) == "" || strings.TrimSpace(in.User.UserID) != strings.TrimSpace(interaction.RequestedBy.UserID) {
			return ReplyResolution{Record: record, Matched: true, Invalid: true}, nil
		}
	}
	selected, valid := selectedOption(request, in.Text)
	if !valid {
		return ReplyResolution{Record: record, Matched: true, Invalid: true}, nil
	}
	answer := questioncmd.Answer{
		Text:           strings.TrimSpace(in.Text),
		SelectedOption: selected,
		AnsweredBy:     in.User,
		AnsweredAt:     zeroOrNow(in.ReceivedAt, s.now().UTC()),
		ProviderMsgID:  strings.TrimSpace(in.MessageID),
	}
	updated, settled, err := s.store.MarkQuestionAnswered(ctx, record.QuestionID, answer)
	return ReplyResolution{Record: updated, Matched: true, Settled: settled}, err
}

func selectedOption(request questioncmd.Request, raw string) (string, bool) {
	text := strings.TrimSpace(raw)
	if len(request.Options) == 0 {
		return "", request.AllowFreeText && text != ""
	}
	for _, option := range request.Options {
		if strings.EqualFold(text, strings.TrimSpace(option.ID)) || strings.EqualFold(text, strings.TrimSpace(option.Label)) {
			return strings.TrimSpace(option.ID), true
		}
	}
	index, err := strconv.Atoi(text)
	if err == nil && index > 0 && index <= len(request.Options) {
		return strings.TrimSpace(request.Options[index-1].ID), true
	}
	if request.AllowFreeText && text != "" {
		return "", true
	}
	return "", false
}

func (s *Service) Timeout(ctx context.Context, questionID string, timedOutAt time.Time) (baldastate.QuestionRecord, bool, error) {
	if s.store == nil {
		return baldastate.QuestionRecord{}, false, fmt.Errorf("question store is required")
	}
	return s.store.MarkQuestionTimedOut(ctx, strings.TrimSpace(questionID), zeroOrNow(timedOutAt, s.now().UTC()))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func zeroOrNow(v time.Time, fallback time.Time) time.Time {
	if v.IsZero() {
		return fallback
	}
	return v.UTC()
}
