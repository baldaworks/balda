package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"
)

type sqliteScheduledJobStore struct {
	db *sql.DB
}

func (s *sqliteScheduledJobStore) Upsert(ctx context.Context, record ScheduledJobRecord) error {
	jobID := strings.TrimSpace(record.JobID)
	if jobID == "" {
		return fmt.Errorf("job_id is required")
	}
	channelType := strings.TrimSpace(record.ChannelType)
	if channelType == "" {
		return fmt.Errorf("channel_type is required")
	}
	addressKey := strings.TrimSpace(record.AddressKey)
	if addressKey == "" {
		return fmt.Errorf("address_key is required")
	}
	addressJSON := strings.TrimSpace(record.AddressJSON)
	if addressJSON == "" {
		return fmt.Errorf("address_json is required")
	}
	prompt := strings.TrimSpace(record.Prompt)
	if prompt == "" {
		return fmt.Errorf("prompt is required")
	}
	scheduleSpec := strings.TrimSpace(record.ScheduleSpec)
	if scheduleSpec == "" {
		return fmt.Errorf("schedule_spec is required")
	}
	if record.NextRunAt.IsZero() {
		return fmt.Errorf("next_run_at is required")
	}

	status := strings.TrimSpace(record.Status)
	if status == "" {
		status = ScheduledJobStatusActive
	}
	timezone := strings.TrimSpace(record.Timezone)
	if timezone == "" {
		timezone = "UTC"
	}
	maxRetries := record.MaxRetries
	if maxRetries < 0 {
		maxRetries = 0
	}
	retryCount := record.RetryCount
	if retryCount < 0 {
		retryCount = 0
	}

	now := time.Now().UTC()
	createdAt := record.CreatedAt.UTC()
	if createdAt.IsZero() {
		createdAt = now
	}
	updatedAt := now

	if _, err := s.db.ExecContext(ctx, `
		INSERT INTO relay_scheduled_jobs (
			job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
			max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			session_id = excluded.session_id,
			channel_type = excluded.channel_type,
			address_key = excluded.address_key,
			address_json = excluded.address_json,
			prompt = excluded.prompt,
			schedule_spec = excluded.schedule_spec,
			timezone = excluded.timezone,
			status = excluded.status,
			max_retries = excluded.max_retries,
			retry_count = excluded.retry_count,
			last_dispatch_key = excluded.last_dispatch_key,
			next_run_at = excluded.next_run_at,
			last_run_at = excluded.last_run_at,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at,
			created_at = relay_scheduled_jobs.created_at`,
		jobID,
		strings.TrimSpace(record.SessionID),
		channelType,
		addressKey,
		addressJSON,
		prompt,
		scheduleSpec,
		timezone,
		status,
		maxRetries,
		retryCount,
		strings.TrimSpace(record.LastDispatchKey),
		record.NextRunAt.UTC().Format(time.RFC3339),
		formatOptionalRFC3339(record.LastRunAt),
		strings.TrimSpace(record.LastError),
		createdAt.Format(time.RFC3339),
		updatedAt.Format(time.RFC3339),
	); err != nil {
		return fmt.Errorf("upsert scheduled job %q: %w", jobID, err)
	}

	return nil
}

func (s *sqliteScheduledJobStore) GetByID(ctx context.Context, jobID string) (ScheduledJobRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM relay_scheduled_jobs
		WHERE job_id = ?`,
		strings.TrimSpace(jobID),
	)

	record, ok, err := scanScheduledJob(row.Scan)
	if err != nil {
		return ScheduledJobRecord{}, false, err
	}
	return record, ok, nil
}

func (s *sqliteScheduledJobStore) List(ctx context.Context) ([]ScheduledJobRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM relay_scheduled_jobs
		ORDER BY job_id ASC`)
	if err != nil {
		return nil, fmt.Errorf("list scheduled jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *sqliteScheduledJobStore) ListByAddress(
	ctx context.Context,
	channelType string,
	addressKey string,
) ([]ScheduledJobRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM relay_scheduled_jobs
		WHERE channel_type = ? AND address_key = ?
		ORDER BY next_run_at ASC`,
		strings.TrimSpace(channelType), strings.TrimSpace(addressKey),
	)
	if err != nil {
		return nil, fmt.Errorf("list scheduled jobs by address: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *sqliteScheduledJobStore) ListDue(ctx context.Context, now time.Time, limit int) ([]ScheduledJobRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, `
		SELECT job_id, session_id, channel_type, address_key, address_json, prompt, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM relay_scheduled_jobs
		WHERE status = ? AND next_run_at <= ?
		ORDER BY next_run_at ASC
		LIMIT ?`,
		ScheduledJobStatusActive,
		now.UTC().Format(time.RFC3339),
		limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due scheduled jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *sqliteScheduledJobStore) Delete(ctx context.Context, jobID string) error {
	trimmed := strings.TrimSpace(jobID)
	if trimmed == "" {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM relay_scheduled_jobs
		WHERE job_id = ?`,
		trimmed,
	); err != nil {
		return fmt.Errorf("delete scheduled job %q: %w", trimmed, err)
	}
	return nil
}

func readScheduledJobs(rows *sql.Rows) ([]ScheduledJobRecord, error) {
	out := make([]ScheduledJobRecord, 0)
	for rows.Next() {
		record, ok, err := scanScheduledJob(rows.Scan)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, record)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate scheduled jobs: %w", err)
	}
	return out, nil
}

func scanScheduledJob(scan func(dest ...any) error) (ScheduledJobRecord, bool, error) {
	var (
		record       ScheduledJobRecord
		nextRunAtRaw string
		lastRunAtRaw string
		createdAtRaw string
		updatedAtRaw string
	)

	err := scan(
		&record.JobID,
		&record.SessionID,
		&record.ChannelType,
		&record.AddressKey,
		&record.AddressJSON,
		&record.Prompt,
		&record.ScheduleSpec,
		&record.Timezone,
		&record.Status,
		&record.MaxRetries,
		&record.RetryCount,
		&record.LastDispatchKey,
		&nextRunAtRaw,
		&lastRunAtRaw,
		&record.LastError,
		&createdAtRaw,
		&updatedAtRaw,
	)
	if err != nil {
		if err == sql.ErrNoRows {
			return ScheduledJobRecord{}, false, nil
		}
		return ScheduledJobRecord{}, false, fmt.Errorf("scan scheduled job: %w", err)
	}

	nextRunAt, err := parseRequiredRFC3339(nextRunAtRaw)
	if err != nil {
		return ScheduledJobRecord{}, false, fmt.Errorf("parse next_run_at: %w", err)
	}
	createdAt, err := parseRequiredRFC3339(createdAtRaw)
	if err != nil {
		return ScheduledJobRecord{}, false, fmt.Errorf("parse created_at: %w", err)
	}
	updatedAt, err := parseRequiredRFC3339(updatedAtRaw)
	if err != nil {
		return ScheduledJobRecord{}, false, fmt.Errorf("parse updated_at: %w", err)
	}
	lastRunAt, err := parseOptionalRFC3339(lastRunAtRaw)
	if err != nil {
		return ScheduledJobRecord{}, false, fmt.Errorf("parse last_run_at: %w", err)
	}

	record.NextRunAt = nextRunAt
	record.LastRunAt = lastRunAt
	record.CreatedAt = createdAt
	record.UpdatedAt = updatedAt

	return record, true, nil
}

func parseRequiredRFC3339(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, fmt.Errorf("timestamp is required")
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func parseOptionalRFC3339(raw string) (time.Time, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, trimmed)
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func formatOptionalRFC3339(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}
