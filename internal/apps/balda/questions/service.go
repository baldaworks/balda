package questions

import (
	"context"
	"fmt"
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
	if s.store == nil {
		return baldastate.QuestionRecord{}, false, fmt.Errorf("question store is required")
	}
	record, ok, err := s.store.GetPendingQuestionByReplyRef(ctx, strings.TrimSpace(in.Provider), strings.TrimSpace(in.ConversationKey), strings.TrimSpace(in.ReplyToMessageID))
	if err != nil || !ok {
		return record, ok, err
	}
	answer := questioncmd.Answer{
		Text:          strings.TrimSpace(in.Text),
		AnsweredBy:    in.User,
		AnsweredAt:    zeroOrNow(in.ReceivedAt, s.now().UTC()),
		ProviderMsgID: strings.TrimSpace(in.MessageID),
	}
	updated, settled, err := s.store.MarkQuestionAnswered(ctx, record.QuestionID, answer)
	if err != nil || !settled {
		return updated, settled, err
	}
	return updated, true, nil
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
