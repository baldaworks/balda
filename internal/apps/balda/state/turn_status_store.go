package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
)

type sqlTurnStatusStore struct {
	db       *sql.DB
	bind     func(string) string
	postgres bool
}

type turnStatusRow struct {
	turnstatuscmd.Snapshot
	completedAt string
}

func (s *sqlTurnStatusStore) Start(ctx context.Context, start turnstatuscmd.Start) (bool, error) {
	if err := validateTurnIdentity(start.Identity); err != nil {
		return false, err
	}
	if start.StartedAt.IsZero() || strings.TrimSpace(start.BootID) == "" {
		return false, fmt.Errorf("turn status start time and boot id are required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	archiveID, found, err := s.archiveID(ctx, tx, start.SessionID)
	if err != nil || !found || archiveID != start.ArchiveID {
		return false, err
	}
	existing, exists, err := s.row(ctx, tx, start.SessionID)
	if err != nil {
		return false, err
	}
	startedAt := start.StartedAt.UTC()
	if exists && existing.ArchiveID == start.ArchiveID && existing.StartedAt.After(startedAt) {
		return false, nil
	}
	now := time.Now().UTC()
	_, err = tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_turn_status
		(session_id, archive_id, turn_id, state, started_at, last_activity_at, activity_kind, completed_at, outcome, boot_id, version, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, '', '', ?, 1, ?)
		ON CONFLICT (session_id) DO UPDATE SET
		archive_id=excluded.archive_id, turn_id=excluded.turn_id, state=excluded.state,
		started_at=excluded.started_at, last_activity_at=excluded.last_activity_at,
		activity_kind=excluded.activity_kind, completed_at='', outcome='', boot_id=excluded.boot_id,
		version=balda_turn_status.version+1, updated_at=excluded.updated_at`),
		start.SessionID, start.ArchiveID, start.TurnID, turnstatuscmd.StateActive,
		formatTurnTime(startedAt), formatTurnTime(startedAt), turnstatuscmd.ActivityStart,
		start.BootID, formatTurnTime(now))
	if err != nil {
		return false, fmt.Errorf("start turn status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit turn status start: %w", err)
	}
	return true, nil
}

func (s *sqlTurnStatusStore) Observe(ctx context.Context, activity turnstatuscmd.Activity) (bool, error) {
	if err := validateTurnIdentity(activity.Identity); err != nil {
		return false, err
	}
	if activity.OccurredAt.IsZero() || !validActivityKind(activity.Kind) || activity.Kind == turnstatuscmd.ActivityStart || activity.Kind == turnstatuscmd.ActivityTerminal {
		return false, fmt.Errorf("valid non-terminal turn activity is required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	row, found, err := s.row(ctx, tx, activity.SessionID)
	if err != nil || !found || row.State != turnstatuscmd.StateActive || row.Identity != activity.Identity {
		return false, err
	}
	when := activity.OccurredAt.UTC()
	if when.Before(row.LastActivityAt) {
		return false, nil
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_turn_status
		SET last_activity_at=?, activity_kind=?, version=version+1, updated_at=?
		WHERE session_id=? AND archive_id=? AND turn_id=? AND state='active'`),
		formatTurnTime(when), activity.Kind, formatTurnTime(time.Now().UTC()), activity.SessionID, activity.ArchiveID, activity.TurnID)
	if err != nil {
		return false, fmt.Errorf("observe turn status: %w", err)
	}
	return commitChanged(tx, result, "turn status activity")
}

func (s *sqlTurnStatusStore) Complete(ctx context.Context, completion turnstatuscmd.Completion) (bool, error) {
	if err := validateTurnIdentity(completion.Identity); err != nil {
		return false, err
	}
	if completion.CompletedAt.IsZero() || !validOutcome(completion.Outcome) {
		return false, fmt.Errorf("valid turn completion is required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	row, found, err := s.row(ctx, tx, completion.SessionID)
	if err != nil || !found || row.State != turnstatuscmd.StateActive || row.Identity != completion.Identity {
		return false, err
	}
	completedAt := completion.CompletedAt.UTC()
	if completedAt.Before(row.StartedAt) {
		completedAt = row.StartedAt
	}
	lastActivityAt := completedAt
	if row.LastActivityAt.After(lastActivityAt) {
		lastActivityAt = row.LastActivityAt
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_turn_status
		SET state='terminal', last_activity_at=?, activity_kind='terminal', completed_at=?, outcome=?, version=version+1, updated_at=?
		WHERE session_id=? AND archive_id=? AND turn_id=? AND state='active'`),
		formatTurnTime(lastActivityAt), formatTurnTime(completedAt), completion.Outcome, formatTurnTime(time.Now().UTC()),
		completion.SessionID, completion.ArchiveID, completion.TurnID)
	if err != nil {
		return false, fmt.Errorf("complete turn status: %w", err)
	}
	return commitChanged(tx, result, "turn status completion")
}

func (s *sqlTurnStatusStore) Snapshot(ctx context.Context, sessionID, currentBootID string) (turnstatuscmd.Snapshot, bool, error) {
	if strings.TrimSpace(sessionID) == "" {
		return turnstatuscmd.Snapshot{}, false, fmt.Errorf("session id is required")
	}
	tx, err := s.begin(ctx)
	if err != nil {
		return turnstatuscmd.Snapshot{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	archiveID, found, err := s.archiveID(ctx, tx, sessionID)
	if err != nil || !found {
		return turnstatuscmd.Snapshot{}, false, err
	}
	row, found, err := s.row(ctx, tx, sessionID)
	if err != nil || !found {
		return turnstatuscmd.Snapshot{}, false, err
	}
	if row.ArchiveID != archiveID {
		if _, err := tx.ExecContext(ctx, s.bind(`DELETE FROM balda_turn_status WHERE session_id=?`), sessionID); err != nil {
			return turnstatuscmd.Snapshot{}, false, fmt.Errorf("delete stale turn status: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return turnstatuscmd.Snapshot{}, false, err
		}
		return turnstatuscmd.Snapshot{}, false, nil
	}
	if row.State == turnstatuscmd.StateActive && currentBootID != "" && row.BootID != currentBootID {
		row.State = turnstatuscmd.StateTerminal
		row.ActivityKind = turnstatuscmd.ActivityTerminal
		row.Outcome = turnstatuscmd.OutcomeInterrupted
		row.CompletedAt = time.Time{}
		row.Version++
		_, err = tx.ExecContext(ctx, s.bind(`UPDATE balda_turn_status SET state='terminal', activity_kind='terminal', completed_at='', outcome=?, version=version+1, updated_at=? WHERE session_id=? AND archive_id=? AND turn_id=? AND state='active'`),
			turnstatuscmd.OutcomeInterrupted, formatTurnTime(time.Now().UTC()), row.SessionID, row.ArchiveID, row.TurnID)
		if err != nil {
			return turnstatuscmd.Snapshot{}, false, fmt.Errorf("reconcile interrupted turn: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return turnstatuscmd.Snapshot{}, false, err
	}
	return row.Snapshot, true, nil
}

func (s *sqlTurnStatusStore) ClearSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	if _, err := s.db.ExecContext(ctx, s.bind(`DELETE FROM balda_turn_status WHERE session_id=?`), sessionID); err != nil {
		return fmt.Errorf("clear turn status: %w", err)
	}
	return nil
}

func (s *sqlTurnStatusStore) begin(ctx context.Context) (*sql.Tx, error) {
	if s.postgres {
		return beginPostgresTx(s.db, ctx, nil)
	}
	return s.db.BeginTx(ctx, nil)
}

func (s *sqlTurnStatusStore) archiveID(ctx context.Context, tx *sql.Tx, sessionID string) (string, bool, error) {
	query := `SELECT archive_id FROM balda_session_metadata WHERE session_id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var archiveID string
	if err := tx.QueryRowContext(ctx, s.bind(query), sessionID).Scan(&archiveID); err != nil {
		if err == sql.ErrNoRows {
			return "", false, nil
		}
		return "", false, fmt.Errorf("read session archive: %w", err)
	}
	return archiveID, true, nil
}

func (s *sqlTurnStatusStore) row(ctx context.Context, tx *sql.Tx, sessionID string) (turnStatusRow, bool, error) {
	query := `SELECT archive_id, turn_id, state, started_at, last_activity_at, activity_kind, completed_at, outcome, boot_id, version FROM balda_turn_status WHERE session_id=?`
	if s.postgres {
		query += ` FOR UPDATE`
	}
	var row turnStatusRow
	var state, activity, outcome, startedAt, lastActivityAt string
	var version int64
	if err := tx.QueryRowContext(ctx, s.bind(query), sessionID).Scan(&row.ArchiveID, &row.TurnID, &state, &startedAt, &lastActivityAt, &activity, &row.completedAt, &outcome, &row.BootID, &version); err != nil {
		if err == sql.ErrNoRows {
			return turnStatusRow{}, false, nil
		}
		return turnStatusRow{}, false, fmt.Errorf("read turn status: %w", err)
	}
	row.SessionID = sessionID
	row.State = turnstatuscmd.State(state)
	row.ActivityKind = turnstatuscmd.ActivityKind(activity)
	row.Outcome = turnstatuscmd.Outcome(outcome)
	row.Version = uint64(version)
	var err error
	if row.StartedAt, err = parseTurnTime(startedAt); err != nil {
		return turnStatusRow{}, false, err
	}
	if row.LastActivityAt, err = parseTurnTime(lastActivityAt); err != nil {
		return turnStatusRow{}, false, err
	}
	if row.completedAt != "" {
		if row.CompletedAt, err = parseTurnTime(row.completedAt); err != nil {
			return turnStatusRow{}, false, err
		}
	}
	return row, true, nil
}

func validateTurnIdentity(identity turnstatuscmd.Identity) error {
	if strings.TrimSpace(identity.SessionID) == "" || strings.TrimSpace(identity.ArchiveID) == "" || strings.TrimSpace(identity.TurnID) == "" {
		return fmt.Errorf("session, archive, and turn ids are required")
	}
	return nil
}

func validActivityKind(kind turnstatuscmd.ActivityKind) bool {
	switch kind {
	case turnstatuscmd.ActivityStart, turnstatuscmd.ActivityProvider, turnstatuscmd.ActivityToolCall,
		turnstatuscmd.ActivityToolResult, turnstatuscmd.ActivityProgress, turnstatuscmd.ActivityTerminal:
		return true
	default:
		return false
	}
}

func validOutcome(outcome turnstatuscmd.Outcome) bool {
	switch outcome {
	case turnstatuscmd.OutcomeSucceeded, turnstatuscmd.OutcomeFailed, turnstatuscmd.OutcomeCanceled,
		turnstatuscmd.OutcomeInterrupted, turnstatuscmd.OutcomeUnknown:
		return true
	default:
		return false
	}
}

func formatTurnTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTurnTime(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse turn status time: %w", err)
	}
	return parsed, nil
}

func commitChanged(tx *sql.Tx, result sql.Result, operation string) (bool, error) {
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("inspect %s: %w", operation, err)
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit %s: %w", operation, err)
	}
	return changed == 1, nil
}
