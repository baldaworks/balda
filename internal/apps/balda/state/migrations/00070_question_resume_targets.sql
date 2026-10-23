-- +goose Up
ALTER TABLE balda_questions ADD COLUMN resume_target_kind TEXT NOT NULL DEFAULT '';
ALTER TABLE balda_questions ADD COLUMN resume_target_key TEXT NOT NULL DEFAULT '';
CREATE INDEX balda_questions_conversation_wait_idx
    ON balda_questions(session_id, status, resume_target_kind, resume_target_key, created_at DESC);

-- +goose Down
DROP INDEX balda_questions_conversation_wait_idx;
ALTER TABLE balda_questions DROP COLUMN resume_target_key;
ALTER TABLE balda_questions DROP COLUMN resume_target_kind;
