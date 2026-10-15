package state

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlScheduleManagementStore struct {
	users *sqlUserStore
}

var _ ScheduleManagementStore = (*sqlScheduleManagementStore)(nil)

func (s *sqlScheduleManagementStore) CheckAuthority(ctx context.Context, a schedulecmd.Authority) error {
	if err := validateScheduleAuthority(a); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.checkAuthority(ctx, tx, a); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return schedulecmd.ErrUnavailable
	}
	return nil
}

func (s *sqlScheduleManagementStore) Save(ctx context.Context, m ScheduleMutation) error {
	if err := validateScheduleMutation(m); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := s.checkAuthority(ctx, tx, m.Authority)
	if err != nil {
		return err
	}
	var source string
	var version uint64
	var deleted, enabled int
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT source, definition_version, deleted, enabled
		FROM balda_scheduled_jobs WHERE job_id = ?`)+s.users.forUpdate, m.Record.JobID).
		Scan(&source, &version, &deleted, &enabled)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return schedulecmd.ErrUnavailable
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if m.Kind == ScheduleCreate {
		if found {
			return schedulecmd.ErrConflict
		}
		if err := s.insert(ctx, tx, m.Record); err != nil {
			return err
		}
	} else {
		if !found {
			return schedulecmd.ErrNotFound
		}
		if source != ScheduledJobSourceManaged {
			return schedulecmd.ErrForbidden
		}
		if deleted == 1 || version != m.ExpectedVersion {
			return schedulecmd.ErrConflict
		}
		if m.Kind == ScheduleEdit && m.Record.Enabled != (enabled == 1) {
			return schedulecmd.ErrConflict
		}
		if err := s.update(ctx, tx, m); err != nil {
			return err
		}
	}
	audit := m.Audit
	audit.Reason = scheduleMutationReason(m)
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return schedulecmd.ErrUnavailable
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return schedulecmd.ErrUnavailable
	}
	return nil
}

// AdmitManualRun makes the run intent and audit event one authority-checked write.
func (s *sqlScheduleManagementStore) AdmitManualRun(ctx context.Context, a ScheduleManualAdmission) (bool, error) {
	if err := validateScheduleAuthority(a.Authority); err != nil {
		return false, err
	}
	if err := validateScheduleRun(a.Run); err != nil || a.Run.Trigger != ScheduleRunTriggerManual ||
		a.Run.DefinitionVersion != a.ExpectedVersion || a.ExpectedVersion == 0 ||
		(a.Run.DispatchState != ScheduleRunPending &&
			(a.Run.DispatchState != ScheduleRunFailed || a.Run.SafeFailureCode != "report_alias_unavailable")) {
		return false, schedulecmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(a.Audit); err != nil ||
		a.Audit.Action != usercmd.AuditActionScheduleRunRequested ||
		a.Audit.TargetType != usercmd.AuditTargetSchedule || a.Audit.TargetID != a.Run.ScheduleID ||
		a.Audit.ActorUserID != a.Authority.UserID || a.Audit.ActorSessionID != a.Authority.SessionID ||
		a.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !a.Audit.OccurredAt.Equal(a.Authority.At) {
		return false, schedulecmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := s.checkAuthority(ctx, tx, a.Authority)
	if err != nil {
		return false, err
	}
	var source string
	var version uint64
	var deleted, enabled int
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT source, definition_version, deleted, enabled
		FROM balda_scheduled_jobs WHERE job_id = ?`)+s.users.forUpdate, a.Run.ScheduleID).
		Scan(&source, &version, &deleted, &enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, schedulecmd.ErrNotFound
	}
	if err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	var existing string
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT run_id FROM balda_schedule_runs
		WHERE schedule_id = ? AND trigger_key = ?`), a.Run.ScheduleID, a.Run.TriggerKey).Scan(&existing)
	if err == nil {
		if err := authority.checkTime(a.Authority); err != nil {
			return false, err
		}
		return false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, schedulecmd.ErrUnavailable
	}
	if source == ScheduledJobSourceInternal || deleted == 1 {
		return false, schedulecmd.ErrNotFound
	}
	if version != a.ExpectedVersion {
		return false, schedulecmd.ErrConflict
	}
	if enabled == 0 && !a.ConfirmDisabled {
		return false, schedulecmd.ErrConflict
	}
	if err := authority.checkTime(a.Authority); err != nil {
		return false, err
	}
	r := a.Run
	r.Version = 1
	r.CreatedAt = time.Now().UTC()
	r.UpdatedAt = r.CreatedAt
	result, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_schedule_runs
		(`+scheduleRunColumns+`)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (schedule_id, trigger_key) DO NOTHING`), scheduleRunValues(r)...)
	if err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	if count == 0 {
		return false, nil
	}
	audit := a.Audit
	audit.Reason = "Schedule run requested"
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	if err := authority.checkTime(a.Authority); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, schedulecmd.ErrUnavailable
	}
	return true, nil
}

func scheduleMutationReason(m ScheduleMutation) string {
	switch m.Kind {
	case ScheduleCreate:
		return "Schedule created"
	case ScheduleEdit:
		return "Schedule edited"
	case ScheduleSelection:
		if m.Record.Enabled {
			return "Schedule enabled"
		}
		return "Schedule disabled"
	case ScheduleDelete:
		return "Schedule deleted"
	default:
		return "Schedule changed"
	}
}

func validateScheduleMutation(m ScheduleMutation) error {
	r, a := m.Record, m.Authority
	if err := validateScheduleAuthority(a); err != nil {
		return err
	}
	if strings.TrimSpace(r.JobID) == "" || len(r.JobID) > 128 || r.Source != ScheduledJobSourceManaged ||
		m.ExpectedVersion >= math.MaxInt64 ||
		r.DefinitionVersion != m.ExpectedVersion+1 || r.NextRunAt.IsZero() ||
		strings.TrimSpace(r.Content) == "" ||
		strings.TrimSpace(r.ScheduleSpec) == "" || r.Deleted && r.Enabled {
		return schedulecmd.ErrInvalid
	}
	hasReportLocator := strings.TrimSpace(r.ReportToChannelType) != "" &&
		strings.TrimSpace(r.ReportToAddressKey) != "" && strings.TrimSpace(r.ReportToAddressJSON) != ""
	hasReportReference := strings.TrimSpace(r.ReportToTargetKind) != "" &&
		strings.TrimSpace(r.ReportToTargetKey) != ""
	if r.ReportToEnabled && !hasReportLocator && !hasReportReference {
		return schedulecmd.ErrInvalid
	}
	if (m.Kind == ScheduleCreate && (m.ExpectedVersion != 0 || r.Deleted || !r.Enabled)) ||
		(m.Kind != ScheduleCreate && m.ExpectedVersion == 0) ||
		(m.Kind != ScheduleCreate && m.Kind != ScheduleEdit && m.Kind != ScheduleSelection && m.Kind != ScheduleDelete) ||
		(m.Kind == ScheduleDelete && (!r.Deleted || r.Enabled)) {
		return schedulecmd.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(m.Audit); err != nil ||
		m.Audit.Action != usercmd.AuditActionScheduleDefinitionChanged ||
		m.Audit.TargetType != usercmd.AuditTargetSchedule || m.Audit.TargetID != r.JobID ||
		m.Audit.ActorUserID != a.UserID || m.Audit.ActorSessionID != a.SessionID ||
		m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !m.Audit.OccurredAt.Equal(a.At) {
		return schedulecmd.ErrInvalid
	}
	return nil
}

func validateScheduleAuthority(a schedulecmd.Authority) error {
	if a.UserID == "" || a.UserVersion == 0 || a.CredentialVersion == 0 ||
		a.SessionID == "" || a.SessionVersion == 0 || a.At.IsZero() {
		return schedulecmd.ErrInvalid
	}
	return nil
}

type lockedScheduleAuthority struct {
	browser lockedBrowserAuthority
}

func (s *sqlScheduleManagementStore) checkAuthority(ctx context.Context, tx *sql.Tx, a schedulecmd.Authority) (lockedScheduleAuthority, error) {
	locked, err := s.users.checkBrowserAuthority(ctx, tx, scheduleBrowserAuthority(a))
	if err != nil {
		return lockedScheduleAuthority{}, scheduleAuthorityError(err)
	}
	return lockedScheduleAuthority{browser: locked}, nil
}

func (a lockedScheduleAuthority) checkTime(request schedulecmd.Authority) error {
	if err := a.browser.checkTime(scheduleBrowserAuthority(request)); err != nil {
		return scheduleAuthorityError(err)
	}
	return nil
}

func scheduleBrowserAuthority(a schedulecmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID,
		userVersion: a.UserVersion, credentialVersion: a.CredentialVersion,
		mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func scheduleAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return schedulecmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return schedulecmd.ErrForbidden
	default:
		return schedulecmd.ErrUnavailable
	}
}

func (s *sqlScheduleManagementStore) insert(ctx context.Context, tx *sql.Tx, r ScheduledJobRecord) error {
	now := time.Now().UTC()
	result, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_scheduled_jobs (
		job_id, session_id, channel_type, address_key, address_json,
		report_to_enabled, report_to_session_id, report_to_channel_type, report_to_address_key, report_to_address_json,
		content, schedule_spec, timezone, status, max_retries, retry_count,
		last_dispatch_key, next_run_at, last_run_at, last_error, created_at, updated_at,
		source, enabled, deleted, definition_version, target_kind, target_key,
		report_to_target_kind, report_to_target_key)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(job_id) DO NOTHING`),
		r.JobID, r.SessionID, r.ChannelType, r.AddressKey, r.AddressJSON,
		boolInt(r.ReportToEnabled), r.ReportToSessionID, r.ReportToChannelType,
		r.ReportToAddressKey, r.ReportToAddressJSON, r.Content, r.ScheduleSpec,
		"UTC", ScheduledJobStatusActive, r.MaxRetries, 0, "",
		r.NextRunAt.UTC().Format(time.RFC3339), "", "",
		now.Format(time.RFC3339), now.Format(time.RFC3339),
		ScheduledJobSourceManaged, 1, 0, r.DefinitionVersion,
		r.TargetKind, r.TargetKey, r.ReportToTargetKind, r.ReportToTargetKey)
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	if count != 1 {
		return schedulecmd.ErrConflict
	}
	return nil
}

func (s *sqlScheduleManagementStore) update(ctx context.Context, tx *sql.Tx, m ScheduleMutation) error {
	r := m.Record
	var query string
	var args []any
	switch m.Kind {
	case ScheduleEdit:
		query = `UPDATE balda_scheduled_jobs SET session_id=?, channel_type=?, address_key=?, address_json=?,
			report_to_enabled=?, report_to_session_id=?, report_to_channel_type=?, report_to_address_key=?, report_to_address_json=?,
			content=?, schedule_spec=?, target_kind=?, target_key=?, report_to_target_kind=?, report_to_target_key=?,
			status=?, retry_count=0, last_dispatch_key='', next_run_at=?, last_error='', definition_version=?, updated_at=?`
		args = []any{r.SessionID, r.ChannelType, r.AddressKey, r.AddressJSON,
			boolInt(r.ReportToEnabled), r.ReportToSessionID, r.ReportToChannelType, r.ReportToAddressKey, r.ReportToAddressJSON,
			r.Content, r.ScheduleSpec, r.TargetKind, r.TargetKey, r.ReportToTargetKind, r.ReportToTargetKey,
			ScheduledJobStatusActive, r.NextRunAt.UTC().Format(time.RFC3339), r.DefinitionVersion, time.Now().UTC().Format(time.RFC3339Nano)}
	case ScheduleSelection:
		if r.Enabled {
			query = `UPDATE balda_scheduled_jobs SET enabled=1, status='active', retry_count=0,
				last_dispatch_key='', next_run_at=?, last_error='', definition_version=?, updated_at=?`
			args = []any{r.NextRunAt.UTC().Format(time.RFC3339), r.DefinitionVersion, time.Now().UTC().Format(time.RFC3339Nano)}
		} else {
			query = `UPDATE balda_scheduled_jobs SET enabled=0, definition_version=?, updated_at=?`
			args = []any{r.DefinitionVersion, time.Now().UTC().Format(time.RFC3339Nano)}
		}
	case ScheduleDelete:
		query = `UPDATE balda_scheduled_jobs SET enabled=0, deleted=1, definition_version=?, updated_at=?`
		args = []any{r.DefinitionVersion, time.Now().UTC().Format(time.RFC3339Nano)}
	default:
		return schedulecmd.ErrInvalid
	}
	query += ` WHERE job_id=? AND source='managed' AND deleted=0 AND definition_version=?`
	args = append(args, r.JobID, m.ExpectedVersion)
	result, err := tx.ExecContext(ctx, s.users.bind(query), args...)
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return schedulecmd.ErrUnavailable
	}
	if count != 1 {
		return schedulecmd.ErrConflict
	}
	return nil
}
