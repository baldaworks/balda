package state

import (
	"context"
	"database/sql"

	"strings"
	"time"
)

type postgresJobStore struct {
	db *sql.DB
}

func (s *postgresJobStore) CreateJob(ctx context.Context, record JobRecord) (bool, error) {
	now := time.Now().UTC()
	normalized, err := normalizeExecutionJob(record, now)
	if err != nil {
		return false, err
	}
	return postgresInsertExecutionJob(ctx, s.db, normalized)
}

func postgresInsertExecutionJob(ctx context.Context, exec contextExecer, normalized JobRecord) (bool, error) {
	res, err := exec.ExecContext(ctx, postgresBind(`
		INSERT INTO execution_jobs (
			id, session_id, parent_job_id, title, objective, status, owner_actor, assigned_actor,
			priority, created_by, result, error,
			created_at, updated_at, started_at, completed_at, canceled_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`), normalized.ID,
		nullIfEmpty(normalized.SessionID),
		nullIfEmpty(normalized.ParentJobID),
		nullIfEmpty(normalized.Title),
		normalized.Objective,
		normalized.Status,
		nullIfEmpty(normalized.OwnerActor),
		nullIfEmpty(normalized.AssignedActor),
		normalized.Priority,
		nullIfEmpty(normalized.CreatedBy),
		nullIfEmpty(normalized.Result),
		nullIfEmpty(normalized.Error),
		normalized.CreatedAt.Format(time.RFC3339),
		normalized.UpdatedAt.Format(time.RFC3339),
		optionalTimeValue(normalized.StartedAt),
		optionalTimeValue(normalized.CompletedAt),
		optionalTimeValue(normalized.CanceledAt),
	)
	if err != nil {
		return false, postgresErrorf("insert runtime job %q: %w", normalized.ID, err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return false, postgresErrorf("count inserted runtime job %q: %w", normalized.ID, err)
	}
	return count > 0, nil
}

func (s *postgresJobStore) CreateJobWithEvent(
	ctx context.Context,
	record JobRecord,
	event JobEventOutboxRecord,
) (bool, error) {
	now := time.Now().UTC()
	normalizedJob, err := normalizeExecutionJob(record, now)
	if err != nil {
		return false, err
	}
	normalizedEvent, err := normalizeJobEventOutbox(event, now)
	if err != nil {
		return false, err
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return false, postgresErrorf("begin create job transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	created, err := postgresInsertExecutionJob(ctx, tx, normalizedJob)
	if err != nil {
		return false, err
	}
	if err := postgresEnqueueJobEvent(ctx, tx, normalizedEvent); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, postgresErrorf("commit create job transaction: %w", err)
	}
	return created, nil
}

func (s *postgresJobStore) GetJob(ctx context.Context, jobID string) (JobRecord, bool, error) {
	record, ok, err := scanExecutionJob(s.db.QueryRowContext(ctx, postgresBind(executionJobSelectSQL+` WHERE id = ?`), strings.TrimSpace(jobID)).Scan)
	if err != nil {
		return JobRecord{}, false, redactPostgresError(err)
	}
	return record, ok, nil
}

func (s *postgresJobStore) ListActiveJobsBySession(ctx context.Context, sessionID string) ([]JobRecord, error) {
	trimmed := strings.TrimSpace(sessionID)
	if trimmed == "" {
		return nil, postgresErrorf("session id is required")
	}
	rows, err := s.db.QueryContext(ctx, postgresBind(executionJobSelectSQL+`
		WHERE session_id = ?
		  AND status NOT IN (?, ?, ?, ?)
		ORDER BY created_at ASC`), trimmed,
		JobStatusCompleted,
		JobStatusFailed,
		JobStatusCanceled,
		JobStatusDeadLettered,
	)
	if err != nil {
		return nil, postgresErrorf("list active runtime jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []JobRecord
	for rows.Next() {
		record, ok, err := scanExecutionJob(rows.Scan)
		if err != nil {
			return nil, redactPostgresError(err)
		}
		if ok {
			out = append(out, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate active runtime jobs: %w", err)
	}
	return out, nil
}

func (s *postgresJobStore) UpdateJobStatus(ctx context.Context, jobID string, status string, reason string) error {
	trimmedJobID := strings.TrimSpace(jobID)
	if trimmedJobID == "" {
		return postgresErrorf("job id is required")
	}
	currentStatus, err := s.currentJobStatus(ctx, trimmedJobID)
	if err != nil {
		return err
	}
	normalizedStatus, err := normalizeExecutionJobStatus(status)
	if err != nil {
		return err
	}
	if err := guardJobStatusTransition(currentStatus, normalizedStatus); err != nil {
		return err
	}
	now := time.Now().UTC()
	startedAt, completedAt, canceledAt := statusTimestamps(normalizedStatus, now)
	_, err = s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_jobs
		SET status = ?,
		    error = ?,
		    updated_at = ?,
		    started_at = COALESCE(started_at, ?),
		    completed_at = COALESCE(completed_at, ?),
		    canceled_at = COALESCE(canceled_at, ?)
		WHERE id = ?`), normalizedStatus,
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		optionalTimeValue(startedAt),
		optionalTimeValue(completedAt),
		optionalTimeValue(canceledAt),
		trimmedJobID,
	)
	if err != nil {
		return postgresErrorf("update runtime job %q status: %w", trimmedJobID, err)
	}
	return nil
}

func (s *postgresJobStore) UpdateJobStatusWithEvent(
	ctx context.Context,
	jobID string,
	status string,
	reason string,
	event JobEventOutboxRecord,
) error {
	normalizedEvent, err := normalizeJobEventOutbox(event, time.Now().UTC())
	if err != nil {
		return err
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin update job transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := postgresUpdateJobStatusTx(ctx, tx, jobID, status, reason); err != nil {
		return err
	}
	if err := postgresEnqueueJobEvent(ctx, tx, normalizedEvent); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit update job transaction: %w", err)
	}
	return nil
}

func postgresUpdateJobStatusTx(ctx context.Context, tx *sql.Tx, jobID string, status string, reason string) error {
	trimmedJobID := strings.TrimSpace(jobID)
	if trimmedJobID == "" {
		return postgresErrorf("job id is required")
	}
	currentStatus, err := postgresCurrentJobStatusTx(ctx, tx, trimmedJobID)
	if err != nil {
		return err
	}
	normalizedStatus, err := normalizeExecutionJobStatus(status)
	if err != nil {
		return err
	}
	if err := guardJobStatusTransition(currentStatus, normalizedStatus); err != nil {
		return err
	}
	now := time.Now().UTC()
	startedAt, completedAt, canceledAt := statusTimestamps(normalizedStatus, now)
	if _, err := tx.ExecContext(ctx, postgresBind(`
		UPDATE execution_jobs
		SET status = ?, error = ?, updated_at = ?,
		    started_at = COALESCE(started_at, ?),
		    completed_at = COALESCE(completed_at, ?),
		    canceled_at = COALESCE(canceled_at, ?)
		WHERE id = ?`), normalizedStatus,
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		optionalTimeValue(startedAt),
		optionalTimeValue(completedAt),
		optionalTimeValue(canceledAt),
		trimmedJobID,
	); err != nil {
		return postgresErrorf("update runtime job %q status: %w", trimmedJobID, err)
	}
	return nil
}

func (s *postgresJobStore) SetJobResult(ctx context.Context, jobID string, result string, status string, reason string) error {
	trimmedJobID := strings.TrimSpace(jobID)
	if trimmedJobID == "" {
		return postgresErrorf("job id is required")
	}
	currentStatus, err := s.currentJobStatus(ctx, trimmedJobID)
	if err != nil {
		return err
	}
	normalizedStatus, err := normalizeExecutionJobStatus(status)
	if err != nil {
		return err
	}
	if err := guardJobStatusTransition(currentStatus, normalizedStatus); err != nil {
		return err
	}
	now := time.Now().UTC()
	startedAt, completedAt, canceledAt := statusTimestamps(normalizedStatus, now)
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_jobs
		SET status = ?,
		    result = ?,
		    error = ?,
		    updated_at = ?,
		    started_at = COALESCE(started_at, ?),
		    completed_at = COALESCE(completed_at, ?),
		    canceled_at = COALESCE(canceled_at, ?)
		WHERE id = ?`), normalizedStatus,
		nullIfEmpty(result),
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		optionalTimeValue(startedAt),
		optionalTimeValue(completedAt),
		optionalTimeValue(canceledAt),
		trimmedJobID,
	); err != nil {
		return postgresErrorf("set runtime job %q result: %w", trimmedJobID, err)
	}
	return nil
}

func (s *postgresJobStore) SetJobResultWithEvent(
	ctx context.Context,
	jobID string,
	resultJSON string,
	status string,
	reason string,
	event JobEventOutboxRecord,
) error {
	normalizedEvent, err := normalizeJobEventOutbox(event, time.Now().UTC())
	if err != nil {
		return err
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin set job result transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := postgresSetJobResultTx(ctx, tx, jobID, resultJSON, status, reason); err != nil {
		return err
	}
	if err := postgresEnqueueJobEvent(ctx, tx, normalizedEvent); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit set job result transaction: %w", err)
	}
	return nil
}

func postgresSetJobResultTx(
	ctx context.Context,
	tx *sql.Tx,
	jobID string,
	result string,
	status string,
	reason string,
) error {
	trimmedJobID := strings.TrimSpace(jobID)
	if trimmedJobID == "" {
		return postgresErrorf("job id is required")
	}
	currentStatus, err := postgresCurrentJobStatusTx(ctx, tx, trimmedJobID)
	if err != nil {
		return err
	}
	normalizedStatus, err := normalizeExecutionJobStatus(status)
	if err != nil {
		return err
	}
	if err := guardJobStatusTransition(currentStatus, normalizedStatus); err != nil {
		return err
	}
	now := time.Now().UTC()
	startedAt, completedAt, canceledAt := statusTimestamps(normalizedStatus, now)
	if _, err := tx.ExecContext(ctx, postgresBind(`
		UPDATE execution_jobs
		SET status = ?, result = ?, error = ?, updated_at = ?,
		    started_at = COALESCE(started_at, ?),
		    completed_at = COALESCE(completed_at, ?),
		    canceled_at = COALESCE(canceled_at, ?)
		WHERE id = ?`), normalizedStatus,
		nullIfEmpty(result),
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		optionalTimeValue(startedAt),
		optionalTimeValue(completedAt),
		optionalTimeValue(canceledAt),
		trimmedJobID,
	); err != nil {
		return postgresErrorf("set runtime job %q result: %w", trimmedJobID, err)
	}
	return nil
}

func (s *postgresJobStore) AppendJobEvent(ctx context.Context, record JobEventRecord) error {
	now := time.Now().UTC()
	normalized, err := normalizeExecutionJobEvent(record, now)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, postgresBind(`
			INSERT INTO execution_job_events (id, job_id, event_type, actor, message_id, payload, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`), normalized.ID,
		normalized.JobID,
		normalized.EventType,
		nullIfEmpty(normalized.Actor),
		nullIfEmpty(normalized.MessageID),
		nullIfEmpty(normalized.Payload),
		normalized.CreatedAt.Format(time.RFC3339),
	); err != nil {
		return postgresErrorf("insert runtime job event %q: %w", normalized.ID, err)
	}
	return nil
}

func (s *postgresJobStore) ListJobEvents(ctx context.Context, jobID string) ([]JobEventRecord, error) {
	trimmed := strings.TrimSpace(jobID)
	if trimmed == "" {
		return nil, postgresErrorf("job id is required")
	}
	rows, err := s.db.QueryContext(ctx, postgresBind(executionJobEventSelectSQL+`
		WHERE job_id = ?
		ORDER BY created_at ASC`), trimmed,
	)
	if err != nil {
		return nil, postgresErrorf("list runtime job events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []JobEventRecord
	for rows.Next() {
		record, err := scanExecutionJobEvent(rows.Scan)
		if err != nil {
			return nil, redactPostgresError(err)
		}
		out = append(out, record)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate runtime job events: %w", err)
	}
	return out, nil
}

func (s *postgresJobStore) EnqueueJobEvent(ctx context.Context, event JobEventOutboxRecord) error {
	normalized, err := normalizeJobEventOutbox(event, time.Now().UTC())
	if err != nil {
		return err
	}
	return postgresEnqueueJobEvent(ctx, s.db, normalized)
}

func postgresEnqueueJobEvent(ctx context.Context, exec contextExecer, event JobEventOutboxRecord) error {
	if _, err := exec.ExecContext(ctx, postgresBind(`
		INSERT INTO execution_job_event_outbox (
			id, job_id, subject, envelope_json, envelope, attempts, last_error, created_at, published_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`), event.ID,
		event.JobID,
		event.Subject,
		event.Envelope,
		event.Envelope,
		event.Attempts,
		nullIfEmpty(event.LastError),
		event.CreatedAt.Format(time.RFC3339),
		optionalTimeValue(event.PublishedAt),
	); err != nil {
		return postgresErrorf("enqueue job event %q: %w", event.ID, err)
	}
	return nil
}

func (s *postgresJobStore) ListPendingJobEvents(ctx context.Context, limit int) ([]JobEventOutboxRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, postgresBind(`
		SELECT id, job_id, subject, COALESCE(envelope, envelope_json, ''), attempts, COALESCE(last_error, ''),
		       created_at, COALESCE(published_at, '')
		FROM execution_job_event_outbox
		WHERE published_at IS NULL
		ORDER BY created_at ASC
		LIMIT ?`), limit)
	if err != nil {
		return nil, postgresErrorf("list pending job events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []JobEventOutboxRecord
	for rows.Next() {
		record, err := scanJobEventOutbox(rows.Scan)
		if err != nil {
			return nil, redactPostgresError(err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate pending job events: %w", err)
	}
	return records, nil
}

func (s *postgresJobStore) MarkJobEventPublished(ctx context.Context, eventID string) error {
	trimmed := strings.TrimSpace(eventID)
	if trimmed == "" {
		return postgresErrorf("job event id is required")
	}
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_job_event_outbox
		SET attempts = attempts + 1, last_error = NULL, published_at = ?
		WHERE id = ?`), time.Now().UTC().Format(time.RFC3339), trimmed); err != nil {
		return postgresErrorf("mark job event %q published: %w", trimmed, err)
	}
	return nil
}

func (s *postgresJobStore) MarkJobEventPublishFailed(ctx context.Context, eventID string, reason string) error {
	trimmed := strings.TrimSpace(eventID)
	if trimmed == "" {
		return postgresErrorf("job event id is required")
	}
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_job_event_outbox
		SET attempts = attempts + 1, last_error = ?
		WHERE id = ?`), nullIfEmpty(reason), trimmed); err != nil {
		return postgresErrorf("mark job event %q publish failed: %w", trimmed, err)
	}
	return nil
}

func (s *postgresJobStore) ReserveDelivery(ctx context.Context, record DeliveryRecord) (DeliveryRecord, bool, error) {
	now := time.Now().UTC()
	normalized, err := normalizeExecutionDelivery(record, now)
	if err != nil {
		return DeliveryRecord{}, false, err
	}
	res, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO execution_delivery_outbox (
			id, delivery_key, job_id, session_id, channel, address_key, kind, payload_json, payload,
			payload_hash, status, provider_message_id, sent_at, error, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`), normalized.ID,
		normalized.DeliveryKey,
		nullIfEmpty(normalized.JobID),
		nullIfEmpty(normalized.SessionID),
		normalized.Channel,
		normalized.AddressKey,
		normalized.Kind,
		normalized.Payload,
		normalized.Payload,
		normalized.PayloadHash,
		normalized.Status,
		nullIfEmpty(normalized.ProviderMessageID),
		optionalTimeValue(normalized.SentAt),
		nullIfEmpty(normalized.Error),
		normalized.CreatedAt.Format(time.RFC3339),
		normalized.UpdatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return DeliveryRecord{}, false, postgresErrorf("reserve runtime delivery %q: %w", normalized.DeliveryKey, err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return DeliveryRecord{}, false, postgresErrorf("count reserved runtime delivery %q: %w", normalized.DeliveryKey, err)
	}
	got, ok, err := s.getDeliveryByKey(ctx, normalized.DeliveryKey)
	if err != nil {
		return DeliveryRecord{}, false, err
	}
	if !ok {
		return DeliveryRecord{}, false, postgresErrorf("reserved runtime delivery %q not found", normalized.DeliveryKey)
	}
	return got, count > 0, nil
}

func (s *postgresJobStore) MarkDeliverySending(ctx context.Context, deliveryKey string) error {
	trimmedKey := strings.TrimSpace(deliveryKey)
	if trimmedKey == "" {
		return postgresErrorf("delivery key is required")
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, postgresBind(`
			UPDATE execution_delivery_outbox
			SET status = ?,
			    error = NULL,
			    updated_at = ?
			WHERE delivery_key = ?`), DeliveryStatusSending,
		now.Format(time.RFC3339),
		trimmedKey,
	); err != nil {
		return postgresErrorf("mark runtime delivery %q sending: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresJobStore) MarkDeliverySent(ctx context.Context, deliveryKey string, providerMessageID string) error {
	trimmedKey := strings.TrimSpace(deliveryKey)
	if trimmedKey == "" {
		return postgresErrorf("delivery key is required")
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_delivery_outbox
		SET status = ?,
		    provider_message_id = ?,
		    sent_at = COALESCE(sent_at, ?),
		    error = NULL,
		    updated_at = ?
		WHERE delivery_key = ?`), DeliveryStatusSent,
		nullIfEmpty(providerMessageID),
		now.Format(time.RFC3339),
		now.Format(time.RFC3339),
		trimmedKey,
	); err != nil {
		return postgresErrorf("mark runtime delivery %q sent: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresJobStore) MarkDeliveryFailed(ctx context.Context, deliveryKey string, reason string) error {
	trimmedKey := strings.TrimSpace(deliveryKey)
	if trimmedKey == "" {
		return postgresErrorf("delivery key is required")
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_delivery_outbox
		SET status = ?,
		    error = ?,
		    updated_at = ?
		WHERE delivery_key = ?`), DeliveryStatusFailed,
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		trimmedKey,
	); err != nil {
		return postgresErrorf("mark runtime delivery %q failed: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresJobStore) ReserveAgentStep(ctx context.Context, record AgentStepRecord) (AgentStepRecord, bool, error) {
	now := time.Now().UTC()
	normalized, err := normalizeExecutionAgentStep(record, now)
	if err != nil {
		return AgentStepRecord{}, false, err
	}
	res, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO execution_agent_steps (
			id, step_key, job_id, agent_name, role, iteration, payload_hash, status,
			result, error, created_at, updated_at, completed_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) ON CONFLICT DO NOTHING`), normalized.ID,
		normalized.StepKey,
		normalized.JobID,
		normalized.AgentName,
		normalized.Role,
		normalized.Iteration,
		normalized.PayloadHash,
		normalized.Status,
		nullIfEmpty(normalized.Result),
		nullIfEmpty(normalized.Error),
		normalized.CreatedAt.Format(time.RFC3339),
		normalized.UpdatedAt.Format(time.RFC3339),
		optionalTimeValue(normalized.CompletedAt),
	)
	if err != nil {
		return AgentStepRecord{}, false, postgresErrorf("reserve runtime agent step %q: %w", normalized.StepKey, err)
	}
	count, err := res.RowsAffected()
	if err != nil {
		return AgentStepRecord{}, false, postgresErrorf("count reserved runtime agent step %q: %w", normalized.StepKey, err)
	}
	got, ok, err := s.getAgentStepByKey(ctx, normalized.StepKey)
	if err != nil {
		return AgentStepRecord{}, false, err
	}
	if !ok {
		return AgentStepRecord{}, false, postgresErrorf("reserved runtime agent step %q not found", normalized.StepKey)
	}
	return got, count > 0, nil
}

func (s *postgresJobStore) CompleteAgentStep(ctx context.Context, stepKey string, result string) error {
	return s.finishAgentStep(ctx, stepKey, AgentStepStatusSucceeded, result, "")
}

func (s *postgresJobStore) FailAgentStep(ctx context.Context, stepKey string, result string, reason string) error {
	return s.finishAgentStep(ctx, stepKey, AgentStepStatusFailed, result, reason)
}

func (s *postgresJobStore) finishAgentStep(ctx context.Context, stepKey string, status string, result string, reason string) error {
	trimmedKey := strings.TrimSpace(stepKey)
	if trimmedKey == "" {
		return postgresErrorf("agent step key is required")
	}
	normalizedStatus, err := normalizeExecutionAgentStepStatus(status)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE execution_agent_steps
		SET status = ?,
		    result = ?,
		    error = ?,
		    updated_at = ?,
		    completed_at = COALESCE(completed_at, ?)
		WHERE step_key = ?`), normalizedStatus,
		nullIfEmpty(result),
		nullIfEmpty(reason),
		now.Format(time.RFC3339),
		now.Format(time.RFC3339),
		trimmedKey,
	); err != nil {
		return postgresErrorf("finish runtime agent step %q: %w", trimmedKey, err)
	}
	return nil
}

func (s *postgresJobStore) getDeliveryByKey(ctx context.Context, deliveryKey string) (DeliveryRecord, bool, error) {
	record, ok, err := scanExecutionDelivery(s.db.QueryRowContext(ctx, postgresBind(executionDeliverySelectSQL+` WHERE delivery_key = ?`), strings.TrimSpace(deliveryKey)).Scan)
	if err != nil {
		return DeliveryRecord{}, false, redactPostgresError(err)
	}
	return record, ok, nil
}

func (s *postgresJobStore) getAgentStepByKey(ctx context.Context, stepKey string) (AgentStepRecord, bool, error) {
	record, ok, err := scanExecutionAgentStep(s.db.QueryRowContext(ctx, postgresBind(executionAgentStepSelectSQL+` WHERE step_key = ?`), strings.TrimSpace(stepKey)).Scan)
	if err != nil {
		return AgentStepRecord{}, false, redactPostgresError(err)
	}
	return record, ok, nil
}

func (s *postgresJobStore) currentJobStatus(ctx context.Context, jobID string) (string, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(`SELECT status FROM execution_jobs WHERE id = ?`), jobID)
	return scanCurrentJobStatus(func(dest ...any) error { return redactPostgresError(row.Scan(dest...)) }, jobID)
}

func postgresCurrentJobStatusTx(ctx context.Context, tx *sql.Tx, jobID string) (string, error) {
	row := tx.QueryRowContext(ctx, postgresBind(`SELECT status FROM execution_jobs WHERE id = ?`), jobID)
	return scanCurrentJobStatus(func(dest ...any) error { return redactPostgresError(row.Scan(dest...)) }, jobID)
}
