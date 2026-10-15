package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

const scheduleRunTimeFormat = "2006-01-02T15:04:05.000000000Z07:00"

const scheduleRunColumns = `run_id, schedule_id, trigger, trigger_key, definition_version,
	version, requested_at, due_at, dispatch_state, attempts, next_attempt_at,
	safe_failure_code, dispatched_at, execution_job_id, payload_json, report_locator_ref, created_at, updated_at`

type sqlScheduleRunStore struct {
	db       *sql.DB
	bind     func(string) string
	postgres bool
}

var _ ScheduleRunStore = (*sqlScheduleRunStore)(nil)

func (s *sqlScheduleRunStore) Create(ctx context.Context, record ScheduleRunRecord) (bool, error) {
	if err := validateScheduleRun(record); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.Version == 0 {
		record.Version = 1
	}
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO balda_schedule_runs
		(`+scheduleRunColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (schedule_id, trigger_key) DO NOTHING`), scheduleRunValues(record)...)
	if err != nil {
		return false, s.failure("create schedule run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, s.failure("count created schedule runs", err)
	}
	return count == 1, nil
}

// CreateCron admits a due slot only while its selected definition is still eligible.
func (s *sqlScheduleRunStore) CreateCron(ctx context.Context, record ScheduleRunRecord, expectedNextRunAt time.Time) (bool, error) {
	if record.Trigger != ScheduleRunTriggerCron || expectedNextRunAt.IsZero() {
		return false, fmt.Errorf("cron schedule run and due slot are required")
	}
	if err := validateScheduleRun(record); err != nil {
		return false, err
	}
	now := time.Now().UTC()
	if record.CreatedAt.IsZero() {
		record.CreatedAt = now
	}
	if record.UpdatedAt.IsZero() {
		record.UpdatedAt = now
	}
	if record.Version == 0 {
		record.Version = 1
	}
	values := append(scheduleRunValues(record), record.ScheduleID, record.DefinitionVersion,
		expectedNextRunAt.UTC().Format(time.RFC3339))
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO balda_schedule_runs
		(`+scheduleRunColumns+`)
		SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
		FROM balda_scheduled_jobs WHERE job_id = ? AND definition_version = ?
		AND next_run_at = ? AND source IN ('config', 'managed')
		AND enabled = 1 AND deleted = 0 AND status = 'active'
		ON CONFLICT (schedule_id, trigger_key) DO NOTHING`), values...)
	if err != nil {
		return false, s.failure("create cron schedule run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, s.failure("count created cron schedule runs", err)
	}
	return count == 1, nil
}

// ClaimCron serializes publication admission with definition changes and leases recovery.
func (s *sqlScheduleRunStore) ClaimCron(
	ctx context.Context, record ScheduleRunRecord, now, leaseUntil time.Time,
) (bool, error) {
	if record.Trigger != ScheduleRunTriggerCron || record.Version == 0 || record.DueAt.IsZero() ||
		now.IsZero() || !leaseUntil.After(now) {
		return false, fmt.Errorf("cron claim requires a selected run and future lease")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, s.failure("begin cron schedule claim", err)
	}
	defer func() { _ = tx.Rollback() }()
	if record.DispatchState != ScheduleRunPublishing {
		// A new selection takes the same row lock as enable/edit/delete.
		// An expired publishing claim is already in flight and may finish.
		locked, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_scheduled_jobs SET updated_at = updated_at
			WHERE job_id = ? AND source IN ('config', 'managed') AND enabled = 1 AND deleted = 0
			AND status = 'active' AND definition_version = ? AND next_run_at = ?
			AND last_dispatch_key <> ?`), record.ScheduleID, record.DefinitionVersion,
			record.DueAt.UTC().Format(time.RFC3339), record.TriggerKey)
		if err != nil {
			return false, s.failure("lock cron schedule selection", err)
		}
		count, err := locked.RowsAffected()
		if err != nil {
			return false, s.failure("count cron schedule selection", err)
		}
		if count != 1 {
			return false, nil
		}
	}
	claimed, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_schedule_runs SET
		dispatch_state = 'publishing', version = version + 1, next_attempt_at = ?, updated_at = ?
		WHERE run_id = ? AND schedule_id = ? AND version = ? AND trigger = 'cron'
		AND ((dispatch_state IN ('pending', 'retrying')
			AND (next_attempt_at = '' OR next_attempt_at <= ?))
			OR (dispatch_state = 'publishing' AND next_attempt_at <= ?))`),
		formatScheduleRunTime(leaseUntil), formatScheduleRunTime(time.Now().UTC()),
		record.RunID, record.ScheduleID, record.Version,
		formatScheduleRunTime(now), formatScheduleRunTime(now))
	if err != nil {
		return false, s.failure("claim cron schedule run", err)
	}
	count, err := claimed.RowsAffected()
	if err != nil {
		return false, s.failure("count claimed cron schedule run", err)
	}
	if count != 1 {
		return false, nil
	}
	if err := tx.Commit(); err != nil {
		return false, s.failure("commit cron schedule claim", err)
	}
	return true, nil
}

func (s *sqlScheduleRunStore) Update(ctx context.Context, record ScheduleRunRecord, expectedVersion uint64) (bool, error) {
	if err := validateScheduleRun(record); err != nil {
		return false, err
	}
	if expectedVersion == 0 {
		return false, fmt.Errorf("expected run version is required")
	}
	record.Version = expectedVersion + 1
	record.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, s.bind(`UPDATE balda_schedule_runs SET
		trigger = ?, trigger_key = ?, definition_version = ?, version = ?, requested_at = ?, due_at = ?,
		dispatch_state = ?, attempts = ?, next_attempt_at = ?, safe_failure_code = ?,
		dispatched_at = ?, execution_job_id = ?, payload_json = ?, report_locator_ref = ?, updated_at = ?
		WHERE run_id = ? AND schedule_id = ? AND version = ?`),
		record.Trigger, record.TriggerKey, record.DefinitionVersion, record.Version,
		formatScheduleRunTime(record.RequestedAt), formatScheduleRunTime(record.DueAt),
		record.DispatchState, record.Attempts, formatScheduleRunTime(record.NextAttemptAt),
		record.SafeFailureCode, formatScheduleRunTime(record.DispatchedAt), record.ExecutionJobID,
		record.PayloadJSON, record.ReportLocatorRef, formatScheduleRunTime(record.UpdatedAt), record.RunID, record.ScheduleID,
		expectedVersion)
	if err != nil {
		return false, s.failure("update schedule run", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, s.failure("count updated schedule runs", err)
	}
	return count == 1, nil
}

func (s *sqlScheduleRunStore) GetByID(ctx context.Context, runID string) (ScheduleRunRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT `+scheduleRunColumns+`
		FROM balda_schedule_runs WHERE run_id = ?`), strings.TrimSpace(runID))
	record, err := scanScheduleRun(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRunRecord{}, false, nil
	}
	if err != nil {
		return ScheduleRunRecord{}, false, s.failure("read schedule run", err)
	}
	return record, true, nil
}

func (s *sqlScheduleRunStore) GetByTriggerKey(
	ctx context.Context, scheduleID, triggerKey string,
) (ScheduleRunRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, s.bind(`SELECT `+scheduleRunColumns+`
		FROM balda_schedule_runs WHERE schedule_id = ? AND trigger_key = ?`),
		strings.TrimSpace(scheduleID), strings.TrimSpace(triggerKey))
	record, err := scanScheduleRun(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return ScheduleRunRecord{}, false, nil
	}
	if err != nil {
		return ScheduleRunRecord{}, false, s.failure("read schedule run by trigger key", err)
	}
	return record, true, nil
}

func (s *sqlScheduleRunStore) ListBySchedule(
	ctx context.Context, scheduleID string, beforeAt time.Time, beforeID string, limit int,
) ([]ScheduleRunRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	before := formatScheduleRunTime(beforeAt)
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+scheduleRunColumns+`
		FROM balda_schedule_runs
		WHERE schedule_id = ? AND (? = '' OR requested_at < ? OR (requested_at = ? AND run_id < ?))
		ORDER BY requested_at DESC, run_id DESC LIMIT ?`),
		strings.TrimSpace(scheduleID), before, before, before, beforeID, limit)
	if err != nil {
		return nil, s.failure("list schedule history", err)
	}
	defer func() { _ = rows.Close() }()
	return s.readRows(rows)
}

func (s *sqlScheduleRunStore) ListPending(ctx context.Context, now time.Time, limit int) ([]ScheduleRunRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+scheduleRunColumns+`
		FROM balda_schedule_runs
		WHERE dispatch_state IN ('pending', 'retrying', 'publishing')
		AND (next_attempt_at = '' OR next_attempt_at <= ?)
		ORDER BY requested_at ASC, run_id ASC LIMIT ?`), formatScheduleRunTime(now), limit)
	if err != nil {
		return nil, s.failure("list pending schedule runs", err)
	}
	defer func() { _ = rows.Close() }()
	return s.readRows(rows)
}

// ListUnclosedDispatched finds runs whose execution settled but cleanup has not.
func (s *sqlScheduleRunStore) ListUnclosedDispatched(
	ctx context.Context, afterAt time.Time, afterID string, limit int,
) ([]ScheduleRunRecord, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	reportEnabled := `COALESCE(json_extract(balda_schedule_runs.payload_json, '$.ReportToEnabled'), 0) = 1`
	if s.postgres {
		reportEnabled = `COALESCE(balda_schedule_runs.payload_json::jsonb ->> 'ReportToEnabled', 'false') = 'true'`
	}
	query := `SELECT ` + scheduleRunColumns + `
		FROM balda_schedule_runs WHERE dispatch_state = 'dispatched' AND closed_at = ''
		AND EXISTS (SELECT 1 FROM execution_jobs job
		  WHERE job.id = balda_schedule_runs.execution_job_id
		  AND job.status IN ('completed', 'failed', 'canceled', 'deadlettered')
		  AND (job.session_id NOT LIKE 'sch-%' OR NOT (` + reportEnabled + `)
		    OR (job.status IN ('canceled', 'deadlettered') AND NOT EXISTS
		      (SELECT 1 FROM execution_delivery_outbox pending
		       WHERE pending.job_id = job.id
		       AND pending.delivery_key IN (job.id || ':delivery:final', job.id || ':delivery:terminal')))
		    OR EXISTS (SELECT 1 FROM execution_delivery_outbox delivery
		      WHERE delivery.job_id = job.id
		      AND delivery.delivery_key IN (job.id || ':delivery:final', job.id || ':delivery:terminal')
		      AND (delivery.status = 'sent' OR
		        (delivery.status = 'failed' AND delivery.error = 'permanent')))))`
	args := make([]any, 0, 3)
	if !afterAt.IsZero() {
		query += ` AND (requested_at > ? OR (requested_at = ? AND run_id > ?))`
		at := formatScheduleRunTime(afterAt)
		args = append(args, at, at, afterID)
	}
	query += ` ORDER BY requested_at ASC, run_id ASC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, s.failure("list unclosed schedule runs", err)
	}
	defer func() { _ = rows.Close() }()
	return s.readRows(rows)
}

// MarkClosed is idempotent so a crash between runtime deletion and this write can retry.
func (s *sqlScheduleRunStore) MarkClosed(ctx context.Context, runID string, at time.Time) error {
	if strings.TrimSpace(runID) == "" || at.IsZero() {
		return fmt.Errorf("run id and close time are required")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE balda_schedule_runs SET closed_at = ?
		WHERE run_id = ? AND dispatch_state = 'dispatched' AND closed_at = ''`),
		formatScheduleRunTime(at), strings.TrimSpace(runID))
	if err != nil {
		return s.failure("mark schedule run closed", err)
	}
	return nil
}

func (s *sqlScheduleRunStore) readRows(rows *sql.Rows) ([]ScheduleRunRecord, error) {
	var records []ScheduleRunRecord
	for rows.Next() {
		record, err := scanScheduleRun(rows.Scan)
		if err != nil {
			return nil, s.failure("scan schedule run", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, s.failure("iterate schedule runs", err)
	}
	return records, nil
}

func (s *sqlScheduleRunStore) failure(operation string, err error) error {
	if s.postgres {
		return postgresErrorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func validateScheduleRun(record ScheduleRunRecord) error {
	if strings.TrimSpace(record.RunID) == "" || strings.TrimSpace(record.ScheduleID) == "" ||
		strings.TrimSpace(record.TriggerKey) == "" || record.DefinitionVersion == 0 || record.RequestedAt.IsZero() {
		return fmt.Errorf("schedule run identity and request time are required")
	}
	if record.Trigger != ScheduleRunTriggerCron && record.Trigger != ScheduleRunTriggerManual {
		return fmt.Errorf("unsupported schedule run trigger")
	}
	if record.DispatchState != ScheduleRunPending && record.DispatchState != ScheduleRunRetrying &&
		record.DispatchState != ScheduleRunPublishing &&
		record.DispatchState != ScheduleRunDispatched && record.DispatchState != ScheduleRunFailed &&
		record.DispatchState != ScheduleRunCanceled {
		return fmt.Errorf("unsupported schedule run state")
	}
	if record.Attempts < 0 {
		return fmt.Errorf("schedule run attempts cannot be negative")
	}
	return nil
}

func scheduleRunValues(record ScheduleRunRecord) []any {
	return []any{
		record.RunID, record.ScheduleID, record.Trigger, record.TriggerKey,
		record.DefinitionVersion, record.Version, formatScheduleRunTime(record.RequestedAt),
		formatScheduleRunTime(record.DueAt), record.DispatchState, record.Attempts,
		formatScheduleRunTime(record.NextAttemptAt), record.SafeFailureCode,
		formatScheduleRunTime(record.DispatchedAt), record.ExecutionJobID, record.PayloadJSON,
		record.ReportLocatorRef,
		formatScheduleRunTime(record.CreatedAt), formatScheduleRunTime(record.UpdatedAt),
	}
}

func scanScheduleRun(scan func(...any) error) (ScheduleRunRecord, error) {
	var record ScheduleRunRecord
	var requested, due, next, dispatched, created, updated string
	err := scan(&record.RunID, &record.ScheduleID, &record.Trigger, &record.TriggerKey,
		&record.DefinitionVersion, &record.Version, &requested, &due, &record.DispatchState,
		&record.Attempts, &next, &record.SafeFailureCode, &dispatched, &record.ExecutionJobID,
		&record.PayloadJSON, &record.ReportLocatorRef, &created, &updated)
	if err != nil {
		return ScheduleRunRecord{}, err
	}
	for _, item := range []struct {
		raw  string
		into *time.Time
	}{
		{requested, &record.RequestedAt}, {due, &record.DueAt},
		{next, &record.NextAttemptAt}, {dispatched, &record.DispatchedAt},
		{created, &record.CreatedAt}, {updated, &record.UpdatedAt},
	} {
		if item.raw == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339Nano, item.raw)
		if err != nil {
			return ScheduleRunRecord{}, fmt.Errorf("parse schedule run time: %w", err)
		}
		*item.into = parsed.UTC()
	}
	return record, nil
}

func formatScheduleRunTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(scheduleRunTimeFormat)
}
