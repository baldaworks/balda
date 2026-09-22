package state

import (
	"context"
	"database/sql"

	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
)

type postgresQuestionStore struct {
	db *sql.DB
}

func (s *postgresQuestionStore) CreatePendingQuestion(ctx context.Context, record QuestionRecord) error {
	if strings.TrimSpace(record.QuestionID) == "" {
		return postgresErrorf("question id is required")
	}
	if strings.TrimSpace(record.SessionID) == "" {
		return postgresErrorf("session id is required")
	}
	if strings.TrimSpace(record.ChannelKind) == "" {
		return postgresErrorf("channel kind is required")
	}
	if strings.TrimSpace(record.AddressKey) == "" {
		return postgresErrorf("address key is required")
	}
	if strings.TrimSpace(record.AddressJSON) == "" {
		return postgresErrorf("address json is required")
	}
	if strings.TrimSpace(record.Prompt) == "" {
		return postgresErrorf("prompt is required")
	}
	if strings.TrimSpace(record.Status) == "" {
		record.Status = questioncmd.StatusPending
	}
	now := time.Now().UTC()
	createdAt := record.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := record.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = now
	}
	_, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_questions (
			question_id, session_id, channel_kind, address_key, address_json,
			prompt, status, interaction_json, resume_json, request_json, answer_json, failure_json,
			provider, conversation_key, provider_message_id, reply_handle, control_handle,
			expires_at, answered_at, failed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), record.QuestionID,
		record.SessionID,
		record.ChannelKind,
		record.AddressKey,
		record.AddressJSON,
		record.Prompt,
		record.Status,
		emptyJSON(record.InteractionJSON),
		emptyJSON(record.ResumeJSON),
		emptyJSON(record.RequestJSON),
		strings.TrimSpace(record.AnswerJSON),
		strings.TrimSpace(record.FailureJSON),
		strings.TrimSpace(record.Provider),
		strings.TrimSpace(record.ConversationKey),
		strings.TrimSpace(record.ProviderMessageID),
		strings.TrimSpace(record.ReplyHandle),
		strings.TrimSpace(record.ControlHandle),
		optionalTimeString(record.ExpiresAt),
		optionalTimeString(record.AnsweredAt),
		optionalTimeString(record.FailedAt),
		createdAt.Format(time.RFC3339),
		updatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return postgresErrorf("insert question %q: %w", record.QuestionID, err)
	}
	return nil
}

func (s *postgresQuestionStore) BindQuestionDeliveryRef(ctx context.Context, questionID string, ref questioncmd.DeliveryRef) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE balda_questions
		SET provider = ?, conversation_key = ?, provider_message_id = ?, reply_handle = ?, control_handle = ?, updated_at = ?
		WHERE question_id = ?`), strings.TrimSpace(ref.Provider),
		strings.TrimSpace(ref.ConversationKey),
		strings.TrimSpace(ref.ProviderMessageID),
		strings.TrimSpace(ref.ReplyHandle),
		strings.TrimSpace(ref.ControlHandle),
		now,
		strings.TrimSpace(questionID),
	)
	if err != nil {
		return postgresErrorf("bind question delivery ref %q: %w", questionID, err)
	}
	return nil
}

func (s *postgresQuestionStore) GetQuestionByID(ctx context.Context, questionID string) (QuestionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(questionSelectSQL+` WHERE question_id = ?`), strings.TrimSpace(questionID))
	record, found, err := scanQuestion(row.Scan)
	return record, found, redactPostgresError(err)
}

func (s *postgresQuestionStore) GetPendingQuestionByReplyRef(ctx context.Context, provider, conversationKey, replyToMessageID string) (QuestionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(questionSelectSQL+`
	 WHERE provider = ? AND conversation_key = ? AND provider_message_id = ? AND status = ?`), strings.TrimSpace(provider),
		strings.TrimSpace(conversationKey),
		strings.TrimSpace(replyToMessageID),
		questioncmd.StatusPending,
	)
	record, found, err := scanQuestion(row.Scan)
	return record, found, redactPostgresError(err)
}

func (s *postgresQuestionStore) MarkQuestionAnswered(ctx context.Context, questionID string, answer questioncmd.Answer) (QuestionRecord, bool, error) {
	answerJSON, err := marshalJSON(answer)
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("marshal question answer: %w", err)
	}
	answeredAt := answer.AnsweredAt.UTC()
	if answeredAt.IsZero() {
		answeredAt = time.Now().UTC()
	}
	return s.updateQuestionResolution(
		ctx,
		questionID,
		`UPDATE balda_questions
		SET status = ?, answer_json = ?, answered_at = ?, updated_at = ?
		WHERE question_id = ? AND status = ?`,
		questioncmd.StatusAnswered,
		answerJSON,
		answeredAt,
		answeredAt,
		"answered",
	)
}

func (s *postgresQuestionStore) MarkQuestionTimedOut(ctx context.Context, questionID string, timedOutAt time.Time) (QuestionRecord, bool, error) {
	at := timedOutAt.UTC()
	if at.IsZero() {
		at = time.Now().UTC()
	}
	result, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE balda_questions
		SET status = ?, answered_at = ?, updated_at = ?
		WHERE question_id = ? AND status = ?`), questioncmd.StatusTimedOut,
		at.Format(time.RFC3339),
		at.Format(time.RFC3339),
		strings.TrimSpace(questionID),
		questioncmd.StatusPending,
	)
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("mark question %q timed out: %w", questionID, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("count timed out question %q: %w", questionID, err)
	}
	record, ok, err := s.GetQuestionByID(ctx, questionID)
	if err != nil {
		return QuestionRecord{}, false, err
	}
	return record, affected > 0 && ok, nil
}

func (s *postgresQuestionStore) MarkQuestionFailed(ctx context.Context, questionID string, failure questioncmd.Failure) (QuestionRecord, bool, error) {
	failureJSON, err := marshalJSON(failure)
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("marshal question failure: %w", err)
	}
	failedAt := failure.FailedAt.UTC()
	if failedAt.IsZero() {
		failedAt = time.Now().UTC()
	}
	return s.updateQuestionResolution(
		ctx,
		questionID,
		`UPDATE balda_questions
		SET status = ?, failure_json = ?, failed_at = ?, updated_at = ?
		WHERE question_id = ? AND status = ?`,
		questioncmd.StatusFailed,
		failureJSON,
		failedAt,
		failedAt,
		"failed",
	)
}

func (s *postgresQuestionStore) updateQuestionResolution(
	ctx context.Context,
	questionID string,
	query string,
	status string,
	payload string,
	recordedAt time.Time,
	updatedAt time.Time,
	action string,
) (QuestionRecord, bool, error) {
	result, err := s.db.ExecContext(ctx, postgresBind(query), status,
		payload,
		recordedAt.Format(time.RFC3339),
		updatedAt.Format(time.RFC3339),
		strings.TrimSpace(questionID),
		questioncmd.StatusPending,
	)
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("mark question %q %s: %w", questionID, action, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return QuestionRecord{}, false, postgresErrorf("count %s question %q: %w", action, questionID, err)
	}
	record, ok, err := s.GetQuestionByID(ctx, questionID)
	if err != nil {
		return QuestionRecord{}, false, err
	}
	return record, affected > 0 && ok, nil
}
