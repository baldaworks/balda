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
	safe_failure_code, dispatched_at, execution_job_id, payload_json, created_at, updated_at`

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
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
		dispatched_at = ?, execution_job_id = ?, payload_json = ?, updated_at = ?
		WHERE run_id = ? AND schedule_id = ? AND version = ?`),
		record.Trigger, record.TriggerKey, record.DefinitionVersion, record.Version,
		formatScheduleRunTime(record.RequestedAt), formatScheduleRunTime(record.DueAt),
		record.DispatchState, record.Attempts, formatScheduleRunTime(record.NextAttemptAt),
		record.SafeFailureCode, formatScheduleRunTime(record.DispatchedAt), record.ExecutionJobID,
		record.PayloadJSON, formatScheduleRunTime(record.UpdatedAt), record.RunID, record.ScheduleID,
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
		WHERE dispatch_state IN ('pending', 'retrying')
		AND (next_attempt_at = '' OR next_attempt_at <= ?)
		ORDER BY requested_at ASC, run_id ASC LIMIT ?`), formatScheduleRunTime(now), limit)
	if err != nil {
		return nil, s.failure("list pending schedule runs", err)
	}
	defer func() { _ = rows.Close() }()
	return s.readRows(rows)
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
		formatScheduleRunTime(record.CreatedAt), formatScheduleRunTime(record.UpdatedAt),
	}
}

func scanScheduleRun(scan func(...any) error) (ScheduleRunRecord, error) {
	var record ScheduleRunRecord
	var requested, due, next, dispatched, created, updated string
	err := scan(&record.RunID, &record.ScheduleID, &record.Trigger, &record.TriggerKey,
		&record.DefinitionVersion, &record.Version, &requested, &due, &record.DispatchState,
		&record.Attempts, &next, &record.SafeFailureCode, &dispatched, &record.ExecutionJobID,
		&record.PayloadJSON, &created, &updated)
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
