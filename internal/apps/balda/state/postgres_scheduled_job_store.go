package state

import (
	"context"
	"database/sql"

	"strings"
	"time"
)

type postgresScheduledJobStore struct {
	db *sql.DB
}

func (s *postgresScheduledJobStore) Upsert(ctx context.Context, record ScheduledJobRecord) error {
	jobID := strings.TrimSpace(record.JobID)
	if jobID == "" {
		return postgresErrorf("job id is required")
	}
	channelType := strings.TrimSpace(record.ChannelType)
	if channelType == "" {
		return postgresErrorf("channel_type is required")
	}
	addressKey := strings.TrimSpace(record.AddressKey)
	if addressKey == "" {
		return postgresErrorf("address_key is required")
	}
	addressJSON := strings.TrimSpace(record.AddressJSON)
	if addressJSON == "" {
		return postgresErrorf("address_json is required")
	}
	content := strings.TrimSpace(record.Content)
	if content == "" {
		return postgresErrorf("content is required")
	}
	scheduleSpec := strings.TrimSpace(record.ScheduleSpec)
	if scheduleSpec == "" {
		return postgresErrorf("schedule_spec is required")
	}
	if record.NextRunAt.IsZero() {
		return postgresErrorf("next_run_at is required")
	}

	status := strings.TrimSpace(record.Status)
	if status == "" {
		status = ScheduledJobStatusActive
	}
	timezone := strings.TrimSpace(record.Timezone)
	if timezone == "" {
		timezone = postgresTimezone
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

	if _, err := s.db.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_scheduled_jobs (
			job_id, session_id, channel_type, address_key, address_json,
			report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
			content, schedule_spec, timezone, status,
			max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO UPDATE SET
			session_id = excluded.session_id,
			channel_type = excluded.channel_type,
			address_key = excluded.address_key,
			address_json = excluded.address_json,
			report_to_enabled = excluded.report_to_enabled,
			report_to_session_id = excluded.report_to_session_id,
			report_to_channel_type = excluded.report_to_channel_type,
			report_to_address_key = excluded.report_to_address_key,
			report_to_address_json = excluded.report_to_address_json,
			content = excluded.content,
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
			created_at = balda_scheduled_jobs.created_at`), jobID,
		strings.TrimSpace(record.SessionID),
		channelType,
		addressKey,
		addressJSON,
		postgresBool(record.ReportToEnabled),
		strings.TrimSpace(record.ReportToSessionID),
		strings.TrimSpace(record.ReportToChannelType),
		strings.TrimSpace(record.ReportToAddressKey),
		strings.TrimSpace(record.ReportToAddressJSON),
		content,
		scheduleSpec,
		timezone,
		status,
		maxRetries,
		retryCount,
		strings.TrimSpace(record.LastDispatchKey),
		record.NextRunAt.UTC().Format(time.RFC3339),
		func() string {
			if record.LastRunAt.IsZero() {
				return ""
			}
			return record.LastRunAt.UTC().Format(time.RFC3339)
		}(),
		strings.TrimSpace(record.LastError),
		createdAt.Format(time.RFC3339),
		updatedAt.Format(time.RFC3339),
	); err != nil {
		return postgresErrorf("upsert scheduled job %q: %w", jobID, err)
	}

	return nil
}

func (s *postgresScheduledJobStore) GetByID(ctx context.Context, jobID string) (ScheduledJobRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(`
		SELECT job_id, session_id, channel_type, address_key, address_json,
		       report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
		       content, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM balda_scheduled_jobs
		WHERE job_id = ?`), strings.TrimSpace(jobID),
	)

	record, ok, err := scanScheduledJob(row.Scan)
	if err != nil {
		return ScheduledJobRecord{}, false, redactPostgresError(err)
	}
	return record, ok, nil
}

func (s *postgresScheduledJobStore) List(ctx context.Context) ([]ScheduledJobRecord, error) {
	rows, err := s.db.QueryContext(ctx, postgresBind(`
		SELECT job_id, session_id, channel_type, address_key, address_json,
		       report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
		       content, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM balda_scheduled_jobs
		ORDER BY job_id ASC`))
	if err != nil {
		return nil, postgresErrorf("list scheduled jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *postgresScheduledJobStore) ListByAddress(
	ctx context.Context,
	channelType string,
	addressKey string,
) ([]ScheduledJobRecord, error) {
	rows, err := s.db.QueryContext(ctx, postgresBind(`
		SELECT job_id, session_id, channel_type, address_key, address_json,
		       report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
		       content, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM balda_scheduled_jobs
		WHERE channel_type = ? AND address_key = ?
		ORDER BY next_run_at ASC`), strings.TrimSpace(channelType), strings.TrimSpace(addressKey),
	)
	if err != nil {
		return nil, postgresErrorf("list scheduled jobs by address: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *postgresScheduledJobStore) ListDue(ctx context.Context, now time.Time, limit int) ([]ScheduledJobRecord, error) {
	if limit <= 0 {
		limit = 100
	}

	rows, err := s.db.QueryContext(ctx, postgresBind(`
		SELECT job_id, session_id, channel_type, address_key, address_json,
		       report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
		       content, schedule_spec, timezone, status,
		       max_retries, retry_count, last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at
		FROM balda_scheduled_jobs
		WHERE status = ? AND next_run_at <= ?
		ORDER BY next_run_at ASC
		LIMIT ?`), ScheduledJobStatusActive,
		now.UTC().Format(time.RFC3339),
		limit,
	)
	if err != nil {
		return nil, postgresErrorf("list due scheduled jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return readScheduledJobs(rows)
}

func (s *postgresScheduledJobStore) Delete(ctx context.Context, jobID string) error {
	trimmed := strings.TrimSpace(jobID)
	if trimmed == "" {
		return nil
	}

	if _, err := s.db.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_scheduled_jobs
		WHERE job_id = ?`), trimmed,
	); err != nil {
		return postgresErrorf("delete scheduled job %q: %w", trimmed, err)
	}
	return nil
}
