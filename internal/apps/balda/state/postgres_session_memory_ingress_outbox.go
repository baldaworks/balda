package state

import (
	"context"
	"database/sql"

	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/baldaworks/balda/sessionmemory"
)

type postgresSessionMemoryIngressOutboxStore struct{ db *sql.DB }

func (s *postgresSessionMemoryIngressOutboxStore) EnqueueSessionMemoryIngress(ctx context.Context, record sessionmemorycmd.IngressRecord) (sessionmemorycmd.IngressRecord, bool, error) {
	if s == nil || s.db == nil {
		return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("session-memory ingress outbox is unavailable")
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
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("begin session-memory ingress enqueue: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	existing, found, err := postgresLoadIngressRecord(ctx, tx, record.ExportID())
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	if found {
		existingEnvelope, marshalErr := sessionmemorycmd.Marshal(existing.Export)
		if marshalErr != nil || string(existingEnvelope) != string(envelope) {
			return sessionmemorycmd.IngressRecord{}, false, sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress export identity was reused", marshalErr)
		}
		if err := tx.Commit(); err != nil {
			return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("commit session-memory ingress replay: %w", err)
		}
		return existing, false, nil
	}
	var sequence uint64
	if err := tx.QueryRowContext(ctx, postgresBind(`SELECT COALESCE(MAX(scope_sequence), 0) + 1 FROM session_memory_ingress_outbox WHERE scope_key = ?`), scope.Key).Scan(&sequence); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("allocate session-memory ingress scope sequence: %w", err)
	}
	record.ScopeSequence = sequence
	if err := record.Validate(); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, err
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`
		INSERT INTO session_memory_ingress_outbox (
			export_id, scope_key, scope_kind, scope_sequence, subject, envelope_json,
			state, attempts, lease_owner, lease_until, next_attempt_at, last_error, created_at, updated_at, published_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', '', ?, ?, '')`), record.ExportID(), scope.Key, scope.Kind, record.ScopeSequence, record.Export.Subject(), string(envelope),
		record.State, record.Attempts, ingressTimestamp(record.CreatedAt), ingressTimestamp(record.UpdatedAt)); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("insert session-memory ingress record: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return sessionmemorycmd.IngressRecord{}, false, postgresErrorf("commit session-memory ingress enqueue: %w", err)
	}
	return record, true, nil
}

func (s *postgresSessionMemoryIngressOutboxStore) ClaimSessionMemoryIngress(ctx context.Context, owner string, now, leaseUntil time.Time, limit int) ([]sessionmemorycmd.IngressRecord, error) {
	if s == nil || s.db == nil {
		return nil, postgresErrorf("session-memory ingress outbox is unavailable")
	}
	owner = strings.TrimSpace(owner)
	if owner == "" || now.IsZero() || !leaseUntil.After(now) || limit <= 0 || limit > 128 {
		return nil, sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress claim is invalid", nil)
	}
	now, leaseUntil = now.UTC(), leaseUntil.UTC()
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return nil, postgresErrorf("begin session-memory ingress claim: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, postgresBind(`
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
		LIMIT ?`), ingressTimestamp(now), ingressTimestamp(now), limit)
	if err != nil {
		return nil, postgresErrorf("query session-memory ingress claims: %w", err)
	}
	candidates, err := scanIngressRecords(rows)
	if err != nil {
		return nil, redactPostgresError(err)
	}
	for index := range candidates {
		candidate := &candidates[index]
		result, updateErr := tx.ExecContext(ctx, postgresBind(`
			UPDATE session_memory_ingress_outbox
			SET state = 'leased', attempts = attempts + 1, lease_owner = ?, lease_until = ?, next_attempt_at = '', updated_at = ?
			WHERE export_id = ? AND ((state = 'pending' AND (next_attempt_at = '' OR next_attempt_at <= ?)) OR (state = 'leased' AND lease_until <= ?))`), owner, ingressTimestamp(leaseUntil), ingressTimestamp(now), candidate.ExportID(), ingressTimestamp(now), ingressTimestamp(now))
		if updateErr != nil {
			return nil, postgresErrorf("lease session-memory ingress record: %w", updateErr)
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
		return nil, postgresErrorf("commit session-memory ingress claims: %w", err)
	}
	return candidates, nil
}

func (s *postgresSessionMemoryIngressOutboxStore) MarkSessionMemoryIngressPublished(ctx context.Context, exportID, owner string, publishedAt time.Time) error {
	return s.settleSessionMemoryIngress(ctx, exportID, owner, postgresIngressPublished, "", nil, publishedAt)
}

func (s *postgresSessionMemoryIngressOutboxStore) ReleaseSessionMemoryIngress(ctx context.Context, exportID, owner, reason string, terminal bool, nextAttemptAt *time.Time, updatedAt time.Time) error {
	state := ingressStatePending
	if terminal {
		state = ingressStateTerminal
	}
	return s.settleSessionMemoryIngress(ctx, exportID, owner, state, reason, nextAttemptAt, updatedAt)
}

func (s *postgresSessionMemoryIngressOutboxStore) settleSessionMemoryIngress(ctx context.Context, exportID, owner, state, reason string, nextAttemptAt *time.Time, at time.Time) error {
	if s == nil || s.db == nil {
		return postgresErrorf("session-memory ingress outbox is unavailable")
	}
	exportID, owner, reason = strings.TrimSpace(exportID), strings.TrimSpace(owner), strings.TrimSpace(reason)
	if exportID == "" || owner == "" || at.IsZero() || (state != postgresIngressPublished && state != ingressStatePending && state != ingressStateTerminal) || (state == ingressStateTerminal && reason == "") || (state != ingressStatePending && nextAttemptAt != nil) || len(reason) > 512 || strings.ContainsAny(reason, "\r\n") {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress settlement is invalid", nil)
	}
	at = at.UTC()
	publishedAt := ""
	retryAt := ""
	if state == postgresIngressPublished {
		publishedAt = ingressTimestamp(at)
	}
	if nextAttemptAt != nil {
		if nextAttemptAt.IsZero() || nextAttemptAt.Before(at) {
			return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress retry time is invalid", nil)
		}
		retryAt = ingressTimestamp(*nextAttemptAt)
	}
	result, err := s.db.ExecContext(ctx, postgresBind(`
		UPDATE session_memory_ingress_outbox
		SET state = ?, lease_owner = '', lease_until = '', next_attempt_at = ?, last_error = ?, updated_at = ?, published_at = ?
		WHERE export_id = ? AND state = 'leased' AND lease_owner = ?`), state, retryAt, reason, ingressTimestamp(at), publishedAt, exportID, owner)
	if err != nil {
		return postgresErrorf("settle session-memory ingress record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress lease is not owned by this worker", err)
	}
	return nil
}

func (s *postgresSessionMemoryIngressOutboxStore) ReplaySessionMemoryIngress(ctx context.Context, exportID, actor, reason string, replayedAt time.Time) error {
	if s == nil || s.db == nil {
		return postgresErrorf("session-memory ingress outbox is unavailable")
	}
	exportID, actor, reason = strings.TrimSpace(exportID), strings.TrimSpace(actor), strings.TrimSpace(reason)
	if exportID == "" || actor == "" || reason == "" || len(actor) > 128 || len(reason) > 512 || strings.ContainsAny(actor+reason, "\r\n") || replayedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress replay is invalid", nil)
	}
	replayedAt = replayedAt.UTC()
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin session-memory ingress replay: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, postgresBind(`
		UPDATE session_memory_ingress_outbox
		SET state = ?, attempts = 0, lease_owner = '', lease_until = '', next_attempt_at = '', last_error = '', updated_at = ?
		WHERE export_id = ? AND state = ?`), ingressStatePending, ingressTimestamp(replayedAt), exportID, ingressStateTerminal)
	if err != nil {
		return postgresErrorf("replay session-memory ingress record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress record is not terminal", err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`INSERT INTO session_memory_ingress_audit (export_id, action, actor, reason, occurred_at) VALUES (?, 'replay_terminal', ?, ?, ?)`), exportID, actor, reason, ingressTimestamp(replayedAt)); err != nil {
		return postgresErrorf("audit session-memory ingress replay: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit session-memory ingress replay: %w", err)
	}
	return nil
}

func (s *postgresSessionMemoryIngressOutboxStore) SessionMemoryIngressStats(ctx context.Context, now time.Time) (sessionmemorycmd.IngressOutboxStats, error) {
	if s == nil || s.db == nil {
		return sessionmemorycmd.IngressOutboxStats{}, postgresErrorf("session-memory ingress outbox is unavailable")
	}
	if now.IsZero() {
		return sessionmemorycmd.IngressOutboxStats{}, sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress stats time is required", nil)
	}
	var pending, terminal uint64
	var oldest string
	if err := s.db.QueryRowContext(ctx, postgresBind(`
		SELECT
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(MIN(CASE WHEN state = ? THEN created_at END), '')
		FROM session_memory_ingress_outbox`), ingressStatePending, ingressStateTerminal, ingressStatePending).Scan(&pending, &terminal, &oldest); err != nil {
		return sessionmemorycmd.IngressOutboxStats{}, postgresErrorf("query session-memory ingress stats: %w", err)
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

func postgresLoadIngressRecord(ctx context.Context, queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}, exportID string) (sessionmemorycmd.IngressRecord, bool, error) {
	row := queryer.QueryRowContext(ctx, postgresBind(`SELECT export_id, scope_key, scope_kind, scope_sequence, envelope_json, state, attempts, lease_owner, lease_until, next_attempt_at, last_error, created_at, updated_at, published_at FROM session_memory_ingress_outbox WHERE export_id = ?`), exportID)
	record, err := scanIngressRecord(row.Scan)
	if err == sql.ErrNoRows {
		return sessionmemorycmd.IngressRecord{}, false, nil
	}
	if err != nil {
		return sessionmemorycmd.IngressRecord{}, false, redactPostgresError(err)
	}
	return record, true, nil
}
