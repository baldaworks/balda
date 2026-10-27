package state

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"
)

const (
	maxRuntimeOperationPrune = 1000
	runtimeOperationTimeForm = "2006-01-02T15:04:05.000000Z"
)

type sqlRuntimeOperationStore struct{ users *sqlUserStore }

func runtimeOperationError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, ErrRuntimeOperationInvalid) || errors.Is(err, ErrRuntimeOperationConflict) {
		return err
	}
	return ErrRuntimeOperationUnavailable
}

func validRuntimeOperationKind(kind string) bool {
	switch RuntimeOperationKind(kind) {
	case RuntimeOperationKindModel, RuntimeOperationKindProvider, RuntimeOperationKindLifetime:
		return true
	default:
		return false
	}
}

func validRuntimeOperationOutcome(outcome RuntimeOperationOutcome) bool {
	switch outcome {
	case RuntimeOperationOutcomeContextPreserved, RuntimeOperationOutcomeContextReset,
		RuntimeOperationOutcomeAborted, RuntimeOperationOutcomeConflict, RuntimeOperationOutcomeFailed,
		RuntimeOperationOutcomeCleanupIncomplete:
		return true
	default:
		return false
	}
}

func validRuntimeOperationReason(reason RuntimeOperationReason) bool {
	switch reason {
	case "", RuntimeOperationReasonUnsupported, RuntimeOperationReasonInvalid,
		RuntimeOperationReasonFailed, RuntimeOperationReasonCanceled,
		RuntimeOperationReasonDeadlineExceeded, RuntimeOperationReasonGuardConflict,
		RuntimeOperationReasonRecoveryAborted, RuntimeOperationReasonCleanupIncomplete,
		RuntimeOperationReasonIncompatible, RuntimeOperationReasonRejected,
		RuntimeOperationReasonBusy, RuntimeOperationReasonClosed,
		RuntimeOperationReasonUnavailable, RuntimeOperationReasonPreDispatch:
		return true
	default:
		return false
	}
}

func publishesRuntime(outcome RuntimeOperationOutcome) bool {
	return outcome == RuntimeOperationOutcomeContextPreserved || outcome == RuntimeOperationOutcomeContextReset
}

func (s *sqlRuntimeOperationStore) Begin(ctx context.Context, intent RuntimeOperationRecord) (RuntimeOperationRecord, error) {
	intent.CreatedAt = canonicalRuntimeOperationTime(intent.CreatedAt)
	intent.UpdatedAt = canonicalRuntimeOperationTime(intent.UpdatedAt)
	if intent.State != RuntimeOperationPrepared || intent.Outcome != "" || intent.ReasonCode != "" || intent.TerminalAt != nil ||
		!intent.CreatedAt.Equal(intent.UpdatedAt) ||
		!validRuntimeOperationKind(intent.Kind) {
		return RuntimeOperationRecord{}, ErrRuntimeOperationInvalid
	}
	if err := prepareRuntimeOperationRecord(&intent); err != nil {
		return RuntimeOperationRecord{}, ErrRuntimeOperationInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()

	existing, found, err := s.get(ctx, tx, "operation_id = ?", intent.OperationID)
	if err != nil {
		return RuntimeOperationRecord{}, err
	}
	if found {
		if !sameRuntimeOperationIntent(existing, intent) {
			return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
		}
		return existing, nil
	}
	if _, found, err := s.get(ctx, tx, "session_id = ? AND state IN ('prepared','applying')", intent.SessionID); err != nil {
		return RuntimeOperationRecord{}, err
	} else if found {
		return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
	}

	_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_runtime_operations(
		operation_id,session_id,input_hash,kind,state,expected_archive_id,expected_generation,
		target_archive_id,target_snapshot_id,target_provider_binding_json,target_model,
		outcome,reason_code,created_at,updated_at,terminal_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,NULL)`),
		intent.OperationID, intent.SessionID, intent.InputHash, intent.Kind, intent.State,
		intent.ExpectedArchiveID, intent.ExpectedGeneration, intent.TargetArchiveID,
		intent.TargetSnapshotID, intent.TargetProviderBindingJSON, intent.TargetModel,
		intent.Outcome, intent.ReasonCode, runtimeOperationTime(intent.CreatedAt), runtimeOperationTime(intent.UpdatedAt))
	if err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	if err := tx.Commit(); err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	return intent, nil
}

func sameRuntimeOperationIntent(a, b RuntimeOperationRecord) bool {
	return a.OperationID == b.OperationID && a.SessionID == b.SessionID && a.InputHash == b.InputHash &&
		a.Kind == b.Kind && a.ExpectedArchiveID == b.ExpectedArchiveID && a.ExpectedGeneration == b.ExpectedGeneration &&
		a.TargetArchiveID == b.TargetArchiveID && a.TargetSnapshotID == b.TargetSnapshotID &&
		a.TargetProviderBindingJSON == b.TargetProviderBindingJSON && a.TargetModel == b.TargetModel
}

func (s *sqlRuntimeOperationStore) MarkApplying(ctx context.Context, operationID, inputHash string) error {
	if !structuralPublicValue(operationID, maxRuntimeOperationIDBytes) || !validRuntimeInputHash(inputHash) {
		return ErrRuntimeOperationInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return runtimeOperationError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	record, found, err := s.get(ctx, tx, "operation_id = ?", operationID)
	if err != nil {
		return err
	}
	if !found || record.InputHash != inputHash {
		return ErrRuntimeOperationConflict
	}
	if record.State == RuntimeOperationApplying {
		return nil
	}
	if record.State == RuntimeOperationTerminal {
		return ErrRuntimeOperationConflict
	}
	updatedAt := time.Now().UTC()
	if updatedAt.Before(record.UpdatedAt) {
		updatedAt = record.UpdatedAt
	}
	updatedAt = canonicalRuntimeOperationTime(updatedAt)
	result, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_runtime_operations
		SET state='applying',updated_at=? WHERE operation_id=? AND input_hash=? AND state='prepared'`),
		runtimeOperationTime(updatedAt), operationID, inputHash)
	if err != nil {
		return runtimeOperationError(ctx, err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return ErrRuntimeOperationConflict
	}
	if err := tx.Commit(); err != nil {
		return runtimeOperationError(ctx, err)
	}
	return nil
}

func (s *sqlRuntimeOperationStore) Finalize(ctx context.Context, final RuntimeOperationFinalization) (RuntimeOperationRecord, error) {
	final.At = canonicalRuntimeOperationTime(final.At)
	if !structuralPublicValue(final.OperationID, maxRuntimeOperationIDBytes) || !validRuntimeInputHash(final.InputHash) ||
		!validRuntimeOperationOutcome(final.Outcome) || !validRuntimeOperationReason(final.ReasonCode) || final.At.IsZero() {
		return RuntimeOperationRecord{}, ErrRuntimeOperationInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	record, found, err := s.get(ctx, tx, "operation_id = ?", final.OperationID)
	if err != nil {
		return RuntimeOperationRecord{}, err
	}
	if !found || record.InputHash != final.InputHash {
		return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
	}
	if record.State == RuntimeOperationTerminal {
		if record.Outcome != string(final.Outcome) || record.ReasonCode != string(final.ReasonCode) {
			return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
		}
		return record, nil
	}
	if final.At.Before(record.UpdatedAt) {
		return RuntimeOperationRecord{}, ErrRuntimeOperationInvalid
	}
	if publishesRuntime(final.Outcome) && record.State != RuntimeOperationApplying {
		return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
	}

	if publishesRuntime(final.Outcome) {
		var archiveID string
		var generation int64
		err = tx.QueryRowContext(ctx, s.users.bind(`SELECT archive_id,runtime_generation
			FROM balda_session_metadata WHERE session_id = ?`)+s.users.forUpdate, record.SessionID).Scan(&archiveID, &generation)
		if errors.Is(err, sql.ErrNoRows) {
			return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
		}
		if err != nil {
			return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
		}
		if archiveID != record.ExpectedArchiveID || generation < 0 || uint64(generation) != record.ExpectedGeneration {
			return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
		}

		result, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_session_metadata SET
			archive_id=CASE WHEN ?='' THEN archive_id ELSE ? END,
			runtime_snapshot_id=CASE WHEN ?='' THEN runtime_snapshot_id ELSE ? END,
			provider_binding_json=CASE WHEN ?='' THEN provider_binding_json ELSE ? END,
			effective_model=CASE WHEN ?='' THEN effective_model ELSE ? END,
			runtime_generation=runtime_generation+1,runtime_outcome=?,updated_at=?
			WHERE session_id=? AND archive_id=? AND runtime_generation=?`),
			record.TargetArchiveID, record.TargetArchiveID,
			record.TargetSnapshotID, record.TargetSnapshotID,
			record.TargetProviderBindingJSON, record.TargetProviderBindingJSON,
			record.TargetModel, record.TargetModel,
			string(final.Outcome), runtimeOperationTime(final.At), record.SessionID,
			record.ExpectedArchiveID, record.ExpectedGeneration)
		if err != nil {
			return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
		}
		if rows, err := result.RowsAffected(); err != nil || rows != 1 {
			return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
		}
	}
	result, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_runtime_operations SET
		state='terminal',outcome=?,reason_code=?,updated_at=?,terminal_at=?
		WHERE operation_id=? AND input_hash=? AND state IN ('prepared','applying')`),
		string(final.Outcome), string(final.ReasonCode), runtimeOperationTime(final.At),
		runtimeOperationTime(final.At), final.OperationID, final.InputHash)
	if err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	if rows, err := result.RowsAffected(); err != nil || rows != 1 {
		return RuntimeOperationRecord{}, ErrRuntimeOperationConflict
	}
	if err := tx.Commit(); err != nil {
		return RuntimeOperationRecord{}, runtimeOperationError(ctx, err)
	}
	record.State = RuntimeOperationTerminal
	record.Outcome = string(final.Outcome)
	record.ReasonCode = string(final.ReasonCode)
	record.UpdatedAt = final.At
	record.TerminalAt = &final.At
	return record, nil
}

func (s *sqlRuntimeOperationStore) Get(ctx context.Context, operationID string) (RuntimeOperationRecord, bool, error) {
	if !structuralPublicValue(operationID, maxRuntimeOperationIDBytes) {
		return RuntimeOperationRecord{}, false, ErrRuntimeOperationInvalid
	}
	return s.get(ctx, s.users.db, "operation_id = ?", operationID)
}

func (s *sqlRuntimeOperationStore) GetUnresolvedBySession(ctx context.Context, sessionID string) (RuntimeOperationRecord, bool, error) {
	if !structuralPublicValue(sessionID, maxRuntimeSessionIDBytes) {
		return RuntimeOperationRecord{}, false, ErrRuntimeOperationInvalid
	}
	return s.get(ctx, s.users.db, "session_id = ? AND state IN ('prepared','applying')", sessionID)
}

type runtimeOperationQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func (s *sqlRuntimeOperationStore) get(ctx context.Context, q runtimeOperationQuerier, where string, args ...any) (RuntimeOperationRecord, bool, error) {
	query := `SELECT operation_id,session_id,input_hash,kind,state,expected_archive_id,expected_generation,
		target_archive_id,target_snapshot_id,target_provider_binding_json,target_model,outcome,reason_code,
		created_at,updated_at,terminal_at FROM balda_runtime_operations WHERE ` + where + ` LIMIT 1`
	var record RuntimeOperationRecord
	var generation int64
	var state, createdAt, updatedAt string
	var terminalAt sql.NullString
	err := q.QueryRowContext(ctx, s.users.bind(query), args...).Scan(
		&record.OperationID, &record.SessionID, &record.InputHash, &record.Kind, &state,
		&record.ExpectedArchiveID, &generation, &record.TargetArchiveID, &record.TargetSnapshotID,
		&record.TargetProviderBindingJSON, &record.TargetModel, &record.Outcome, &record.ReasonCode,
		&createdAt, &updatedAt, &terminalAt)
	if errors.Is(err, sql.ErrNoRows) {
		return RuntimeOperationRecord{}, false, nil
	}
	if err != nil {
		return RuntimeOperationRecord{}, false, runtimeOperationError(ctx, err)
	}
	if generation < 0 {
		return RuntimeOperationRecord{}, false, ErrRuntimeOperationUnavailable
	}
	record.State = RuntimeOperationState(state)
	record.ExpectedGeneration = uint64(generation)
	record.CreatedAt, err = time.Parse(time.RFC3339Nano, createdAt)
	if err == nil {
		record.UpdatedAt, err = time.Parse(time.RFC3339Nano, updatedAt)
	}
	if err == nil && terminalAt.Valid {
		var parsed time.Time
		parsed, err = time.Parse(time.RFC3339Nano, terminalAt.String)
		record.TerminalAt = &parsed
	}
	rawBinding := record.TargetProviderBindingJSON
	if err != nil || prepareRuntimeOperationRecord(&record) != nil || rawBinding != record.TargetProviderBindingJSON ||
		!validRuntimeOperationKind(record.Kind) ||
		(record.State == RuntimeOperationTerminal && (!validRuntimeOperationOutcome(RuntimeOperationOutcome(record.Outcome)) || !validRuntimeOperationReason(RuntimeOperationReason(record.ReasonCode)))) ||
		(record.State != RuntimeOperationTerminal && (record.Outcome != "" || record.ReasonCode != "")) {
		return RuntimeOperationRecord{}, false, ErrRuntimeOperationUnavailable
	}
	return record, true, nil
}

func (s *sqlRuntimeOperationStore) Prune(ctx context.Context, before time.Time, limit int) (int64, error) {
	if before.IsZero() || limit < 1 || limit > maxRuntimeOperationPrune {
		return 0, ErrRuntimeOperationInvalid
	}
	cutoff := runtimeOperationTime(before)
	tx, err := s.users.begin(ctx)
	if err != nil {
		return 0, runtimeOperationError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()

	canonical := `length(terminal_at)=27 AND substr(terminal_at,20,1)='.' AND substr(terminal_at,27,1)='Z'`
	query := `DELETE FROM balda_runtime_operations
		WHERE operation_id IN (SELECT operation_id FROM balda_runtime_operations
			WHERE state='terminal' AND ` + canonical + ` AND terminal_at < ?
			ORDER BY terminal_at,operation_id LIMIT ?)`
	result, err := tx.ExecContext(ctx, s.users.bind(query), cutoff, limit)
	if err != nil {
		return 0, runtimeOperationError(ctx, err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return 0, ErrRuntimeOperationUnavailable
	}
	remaining := int64(limit) - count
	if remaining > 0 {
		// Migration 71/36 may contain RFC3339 timestamps written before the
		// operation store standardized UTC microseconds. Normalize only that
		// bounded legacy subset; canonical rows retain the indexed text range.
		normalized := `CASE
			WHEN length(terminal_at)=20 AND substr(terminal_at,20,1)='Z'
				THEN substr(terminal_at,1,19)||'.000000Z'
			WHEN length(terminal_at) BETWEEN 22 AND 30 AND substr(terminal_at,20,1)='.' AND substr(terminal_at,length(terminal_at),1)='Z'
				THEN substr(terminal_at,1,20)||substr(substr(terminal_at,21,length(terminal_at)-21)||'000000',1,6)||'Z'
			ELSE NULL END`
		legacyQuery := `DELETE FROM balda_runtime_operations
			WHERE operation_id IN (SELECT operation_id FROM balda_runtime_operations
				WHERE state='terminal' AND NOT (` + canonical + `) AND (` + normalized + `) < ?
				ORDER BY (` + normalized + `),operation_id LIMIT ?)`
		legacyResult, err := tx.ExecContext(ctx, s.users.bind(legacyQuery), cutoff, remaining)
		if err != nil {
			return 0, runtimeOperationError(ctx, err)
		}
		legacyCount, err := legacyResult.RowsAffected()
		if err != nil {
			return 0, ErrRuntimeOperationUnavailable
		}
		count += legacyCount
	}
	if err := tx.Commit(); err != nil {
		return 0, runtimeOperationError(ctx, err)
	}
	return count, nil
}

func validRuntimeInputHash(hash string) bool {
	if len(hash) != 64 || strings.ToLower(hash) != hash {
		return false
	}
	for _, r := range hash {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func canonicalRuntimeOperationTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func runtimeOperationTime(value time.Time) string {
	return canonicalRuntimeOperationTime(value).Format(runtimeOperationTimeForm)
}
