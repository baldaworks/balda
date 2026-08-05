package state

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
)

type sqliteSessionMemoryIngressOutboxStore struct{ db *sql.DB }

const (
	ingressStatePending  = DeliveryStatusPending
	ingressStateTerminal = "terminal"
)

var _ SessionMemoryIngressOutboxStore = (*sqliteSessionMemoryIngressOutboxStore)(nil)

func (s *sqliteSessionMemoryIngressOutboxStore) EnqueueSessionMemoryIngress(ctx context.Context, record sessionmemorycmd.IngressRecord) (sessionmemorycmd.IngressRecord, bool, error) {
	if s == nil || s.db == nil {
		return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	if err := validateNewIngressRecord(record); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	envelope, err := sessionmemorycmd.Marshal(record.Export)
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	scope, err := record.Scope()
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("begin session-memory ingress enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := loadIngressRecord(ctx, tx, record.ExportID())
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	if found {
		existingEnvelope, marshalErr := sessionmemorycmd.Marshal(existing.Export)
		if marshalErr != nil || string(existingEnvelope) != string(envelope) {
			return sessionmemorycmd.IngressRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress export identity was reused", marshalErr)
		}
		if err := tx.Commit(); err != nil {
			return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("commit session-memory ingress replay: %w", err)
		}
		return existing, false, nil
	}
	var sequence uint64
	if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(scope_sequence), 0) + 1 FROM session_memory_ingress_outbox WHERE scope_key = ?`, scope.Key).Scan(&sequence); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("allocate session-memory ingress scope sequence: %w", err)
	}
	record.ScopeSequence = sequence
	if err := record.Validate(); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_memory_ingress_outbox (
			export_id, scope_key, scope_kind, scope_sequence, subject, envelope_json,
			state, attempts, lease_owner, lease_until, next_attempt_at, last_error, created_at, updated_at, published_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', ?, ?, '')`,
		record.ExportID(), scope.Key, scope.Kind, record.ScopeSequence, record.Export.Subject(), string(envelope),
		record.State, record.Attempts, ingressTimestamp(record.CreatedAt), ingressTimestamp(record.UpdatedAt)); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("insert session-memory ingress record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, fmt.Errorf("commit session-memory ingress enqueue: %w", err)
	}
	return record, true, nil
}

func (s *sqliteSessionMemoryIngressOutboxStore) ClaimSessionMemoryIngress(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]sessionmemorycmd.IngressRecord, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || now.IsZero() || !leaseUntil.After(now) || limit <= 0 || limit > 128 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress claim is invalid", nil)
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin session-memory ingress claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT export_id, scope_key, scope_kind, scope_sequence, envelope_json, state, attempts,
			lease_owner, lease_until, next_attempt_at, last_error, created_at, updated_at, published_at
		FROM session_memory_ingress_outbox AS candidate
		WHERE ((candidate.state = 'pending' AND (candidate.next_attempt_at = '' OR candidate.next_attempt_at <= ?))
			OR (candidate.state = 'leased' AND candidate.lease_until <= ?))
			AND NOT EXISTS (
				SELECT 1 FROM session_memory_ingress_outbox AS prior
				WHERE prior.scope_key = candidate.scope_key
					AND prior.scope_sequence < candidate.scope_sequence
					AND prior.state NOT IN ('published', 'terminal')
			)
		ORDER BY candidate.created_at, candidate.scope_key, candidate.scope_sequence
		LIMIT ?`, ingressTimestamp(now), ingressTimestamp(now), limit)
	if err != nil {
		return nil, fmt.Errorf("query session-memory ingress claims: %w", err)
	}
	candidates, err := scanIngressRecords(rows)
	if err != nil {
		return nil, err
	}
	for index := range candidates {
		candidate := &candidates[index]
		result, updateErr := tx.ExecContext(ctx, `
			UPDATE session_memory_ingress_outbox
			SET state = 'leased', attempts = attempts + 1, lease_owner = ?, lease_until = ?, next_attempt_at = '', updated_at = ?
			WHERE export_id = ? AND ((state = 'pending' AND (next_attempt_at = '' OR next_attempt_at <= ?)) OR (state = 'leased' AND lease_until <= ?))`,
			owner, ingressTimestamp(leaseUntil), ingressTimestamp(now), candidate.ExportID(), ingressTimestamp(now), ingressTimestamp(now))
		if updateErr != nil {
			return nil, fmt.Errorf("lease session-memory ingress record: %w", updateErr)
		}
		affected, updateErr := result.RowsAffected()
		if updateErr != nil || affected != 1 {
			return nil, sessionmemory.RetryableError(sessionmemory.CodeConflict, "session-memory ingress record changed while claiming", updateErr)
		}
		candidate.State = sessionmemorycmd.IngressStateLeased
		candidate.Attempts++
		candidate.LeaseOwner = owner
		candidate.LeaseUntil = &leaseUntil
		candidate.UpdatedAt = now
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit session-memory ingress claims: %w", err)
	}
	return candidates, nil
}

func (s *sqliteSessionMemoryIngressOutboxStore) MarkSessionMemoryIngressPublished(ctx context.Context, exportID, owner string, publishedAt time.Time) error {
	return s.settleSessionMemoryIngress(ctx, exportID, owner, "published", "", nil, publishedAt)
}

func (s *sqliteSessionMemoryIngressOutboxStore) ReleaseSessionMemoryIngress(ctx context.Context, exportID, owner, reason string, terminal bool, nextAttemptAt *time.Time, updatedAt time.Time) error {
	state := ingressStatePending
	if terminal {
		state = ingressStateTerminal
	}
	return s.settleSessionMemoryIngress(ctx, exportID, owner, state, reason, nextAttemptAt, updatedAt)
}

func (s *sqliteSessionMemoryIngressOutboxStore) settleSessionMemoryIngress(ctx context.Context, exportID, owner, state, reason string, nextAttemptAt *time.Time, at time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	exportID, owner, reason = strings.TrimSpace(exportID), strings.TrimSpace(owner), strings.TrimSpace(reason)
	if exportID == "" || owner == "" || at.IsZero() || (state != "published" && state != ingressStatePending && state != ingressStateTerminal) || (state == ingressStateTerminal && reason == "") || (state != ingressStatePending && nextAttemptAt != nil) || len(reason) > 512 || strings.ContainsAny(reason, "\r\n") {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress settlement is invalid", nil)
	}
	at = at.UTC()
	publishedAt := ""
	retryAt := ""
	if state == "published" {
		publishedAt = ingressTimestamp(at)
	}
	if nextAttemptAt != nil {
		if nextAttemptAt.IsZero() || nextAttemptAt.Before(at) {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress retry time is invalid", nil)
		}
		retryAt = ingressTimestamp(*nextAttemptAt)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_memory_ingress_outbox
		SET state = ?, lease_owner = '', lease_until = '', next_attempt_at = ?, last_error = ?, updated_at = ?, published_at = ?
		WHERE export_id = ? AND state = 'leased' AND lease_owner = ?`,
		state, retryAt, reason, ingressTimestamp(at), publishedAt, exportID, owner)
	if err != nil {
		return fmt.Errorf("settle session-memory ingress record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress lease is not owned by this worker", err)
	}
	return nil
}

func (s *sqliteSessionMemoryIngressOutboxStore) ReplaySessionMemoryIngress(ctx context.Context, exportID, actor, reason string, replayedAt time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	exportID, actor, reason = strings.TrimSpace(exportID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if exportID == "" || actor == "" || reason == "" || len(actor) > 128 || len(reason) > 512 || strings.ContainsAny(actor+reason, "\r\n") || replayedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress replay is invalid", nil)
	}
	replayedAt = replayedAt.UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin session-memory ingress replay: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
		UPDATE session_memory_ingress_outbox
		SET state = ?, attempts = 0, lease_owner = '', lease_until = '', next_attempt_at = '', last_error = '', updated_at = ?
		WHERE export_id = ? AND state = ?`, ingressStatePending, ingressTimestamp(replayedAt), exportID, ingressStateTerminal)
	if err != nil {
		return fmt.Errorf("replay session-memory ingress record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress record is not terminal", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO session_memory_ingress_audit (export_id, action, actor, reason, occurred_at) VALUES (?, 'replay_terminal', ?, ?, ?)`, exportID, actor, reason, ingressTimestamp(replayedAt)); err != nil {
		return fmt.Errorf("audit session-memory ingress replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit session-memory ingress replay: %w", err)
	}
	return nil
}

func (s *sqliteSessionMemoryIngressOutboxStore) SessionMemoryIngressStats(ctx context.Context, now time.Time) (sessionmemorycmd.IngressOutboxStats, error) {
	if s == nil || s.db == nil {
		return sessionmemorycmd.IngressOutboxStats{}, fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	if now.IsZero() {
		return sessionmemorycmd.IngressOutboxStats{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress stats time is required", nil)
	}
	var pending, terminal uint64
	var oldest string
	if err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(MIN(CASE WHEN state = ? THEN created_at END), '')
		FROM session_memory_ingress_outbox`, ingressStatePending, ingressStateTerminal, ingressStatePending).Scan(&pending, &terminal, &oldest); err != nil {
		return sessionmemorycmd.IngressOutboxStats{}, fmt.Errorf("query session-memory ingress stats: %w", err)
	}
	stats := sessionmemorycmd.IngressOutboxStats{PendingCount: pending, TerminalCount: terminal}
	if oldest == "" {
		return stats, nil
	}
	value, err := parseIngressTimestamp(oldest)
	if err != nil {
		return sessionmemorycmd.IngressOutboxStats{}, err
	}
	stats.OldestPendingAt = &value
	if now.UTC().After(value) {
		stats.OldestPendingAge = now.UTC().Sub(value)
	}
	return stats, nil
}

func validateNewIngressRecord(record sessionmemorycmd.IngressRecord) error {
	if record.SchemaVersion != sessionmemorycmd.IngressSchemaVersionV1 || record.ScopeSequence != 0 || record.State != sessionmemorycmd.IngressStatePending || record.Attempts != 0 || record.LeaseOwner != "" || record.LeaseUntil != nil || record.NextAttemptAt != nil || record.PublishedAt != nil || record.LastError != "" || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "new session-memory ingress record is invalid", nil)
	}
	return record.Export.Validate()
}

func loadIngressRecord(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, exportID string) (sessionmemorycmd.IngressRecord, bool, error) {
	row := queryer.QueryRowContext(ctx, `SELECT export_id, scope_key, scope_kind, scope_sequence, envelope_json, state, attempts, lease_owner, lease_until, next_attempt_at, last_error, created_at, updated_at, published_at FROM session_memory_ingress_outbox WHERE export_id = ?`, exportID)
	record, err := scanIngressRecord(row.Scan)
	if err == sql.ErrNoRows {
		return sessionmemorycmd.IngressRecord{}, false, nil
	}
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	return record, true, nil
}

func scanIngressRecords(rows *sql.Rows) ([]sessionmemorycmd.IngressRecord, error) {
	defer func() { _ = rows.Close() }()
	var records []sessionmemorycmd.IngressRecord
	for rows.Next() {
		record, err := scanIngressRecord(rows.Scan)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session-memory ingress records: %w", err)
	}
	return records, nil
}

func scanIngressRecord(scan func(...any) error) (sessionmemorycmd.IngressRecord, error) {
	var exportID, scopeKey, scopeKind, envelope, state, leaseOwner, leaseUntil, nextAttemptAt, lastError, createdAt, updatedAt, publishedAt string
	var sequence uint64
	var attempts uint32
	if err := scan(&exportID, &scopeKey, &scopeKind, &sequence, &envelope, &state, &attempts, &leaseOwner, &leaseUntil, &nextAttemptAt, &lastError, &createdAt, &updatedAt, &publishedAt); err != nil {
		return sessionmemorycmd.IngressRecord{}, err
	}
	export, err := sessionmemorycmd.Unmarshal([]byte(envelope))
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, fmt.Errorf("decode session-memory ingress export: %w", err)
	}
	if export.ExportID() != exportID {
		return sessionmemorycmd.IngressRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored session-memory ingress identity is invalid", nil)
	}
	created, err := parseIngressTimestamp(createdAt)
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, err
	}
	updated, err := parseIngressTimestamp(updatedAt)
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, err
	}
	record := sessionmemorycmd.IngressRecord{SchemaVersion: sessionmemorycmd.IngressSchemaVersionV1, Export: export, ScopeSequence: sequence, State: sessionmemorycmd.IngressState(state), Attempts: attempts, LeaseOwner: leaseOwner, LastError: lastError, CreatedAt: created, UpdatedAt: updated}
	if leaseUntil != "" {
		lease, parseErr := parseIngressTimestamp(leaseUntil)
		if parseErr != nil {
			return sessionmemorycmd.IngressRecord{}, parseErr
		}
		record.LeaseUntil = &lease
	}
	if nextAttemptAt != "" {
		nextAttempt, parseErr := parseIngressTimestamp(nextAttemptAt)
		if parseErr != nil {
			return sessionmemorycmd.IngressRecord{}, parseErr
		}
		record.NextAttemptAt = &nextAttempt
	}
	if publishedAt != "" {
		published, parseErr := parseIngressTimestamp(publishedAt)
		if parseErr != nil {
			return sessionmemorycmd.IngressRecord{}, parseErr
		}
		record.PublishedAt = &published
	}
	scope, err := record.Scope()
	if err != nil || scope.Key != scopeKey || string(scope.Kind) != scopeKind {
		return sessionmemorycmd.IngressRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored session-memory ingress scope is invalid", err)
	}
	if err := record.Validate(); err != nil {
		return sessionmemorycmd.IngressRecord{}, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "stored session-memory ingress record is invalid", err)
	}
	return record, nil
}

func ingressTimestamp(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseIngressTimestamp(value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse session-memory ingress timestamp: %w", err)
	}
	return parsed.UTC(), nil
}
