-- +goose Up
-- Existing answered rows predate recoverable admission and must not be replayed.
ALTER TABLE balda_questions ADD COLUMN answer_continuation_pending BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE balda_questions ADD COLUMN answer_controls_pending BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX balda_question_answer_handoff ON balda_questions (question_id)
    WHERE answer_continuation_pending = TRUE OR answer_controls_pending = TRUE;

-- +goose Down
-- Handoffs are forward-only; restore a consistent database backup to downgrade.
SELECT * FROM balda_question_answer_handoff_downgrade_requires_database_backup;
