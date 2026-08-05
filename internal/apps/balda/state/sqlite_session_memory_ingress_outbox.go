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
			state, attempts, lease_owner, lease_until, last_error, created_at, updated_at, published_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, '', '', '', ?, ?, '')`,
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
			lease_owner, lease_until, last_error, created_at, updated_at, published_at
		FROM session_memory_ingress_outbox AS candidate
		WHERE (candidate.state = 'pending' OR (candidate.state = 'leased' AND candidate.lease_until <= ?))
			AND NOT EXISTS (
				SELECT 1 FROM session_memory_ingress_outbox AS prior
				WHERE prior.scope_key = candidate.scope_key
					AND prior.scope_sequence < candidate.scope_sequence
					AND prior.state NOT IN ('published', 'terminal')
			)
		ORDER BY candidate.created_at, candidate.scope_key, candidate.scope_sequence
		LIMIT ?`, ingressTimestamp(now), limit)
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
			SET state = 'leased', attempts = attempts + 1, lease_owner = ?, lease_until = ?, updated_at = ?
			WHERE export_id = ? AND (state = 'pending' OR (state = 'leased' AND lease_until <= ?))`,
			owner, ingressTimestamp(leaseUntil), ingressTimestamp(now), candidate.ExportID(), ingressTimestamp(now))
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
	return s.settleSessionMemoryIngress(ctx, exportID, owner, "published", "", publishedAt)
}

func (s *sqliteSessionMemoryIngressOutboxStore) ReleaseSessionMemoryIngress(ctx context.Context, exportID, owner, reason string, terminal bool, updatedAt time.Time) error {
	state := "pending"
	if terminal {
		state = "terminal"
	}
	return s.settleSessionMemoryIngress(ctx, exportID, owner, state, reason, updatedAt)
}

func (s *sqliteSessionMemoryIngressOutboxStore) settleSessionMemoryIngress(ctx context.Context, exportID, owner, state, reason string, at time.Time) error {
	if s == nil || s.db == nil {
		return fmt.Errorf("session-memory ingress outbox is unavailable")
	}
	exportID, owner, reason = strings.TrimSpace(exportID), strings.TrimSpace(owner), strings.TrimSpace(reason)
	if exportID == "" || owner == "" || at.IsZero() || (state != "published" && state != "pending" && state != "terminal") || (state == "terminal" && reason == "") || len(reason) > 512 || strings.ContainsAny(reason, "\r\n") {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "session-memory ingress settlement is invalid", nil)
	}
	at = at.UTC()
	publishedAt := ""
	if state == "published" {
		publishedAt = ingressTimestamp(at)
	}
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_memory_ingress_outbox
		SET state = ?, lease_owner = '', lease_until = '', last_error = ?, updated_at = ?, published_at = ?
		WHERE export_id = ? AND state = 'leased' AND lease_owner = ?`,
		state, reason, ingressTimestamp(at), publishedAt, exportID, owner)
	if err != nil {
		return fmt.Errorf("settle session-memory ingress record: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return sessionmemory.PermanentError(sessionmemory.CodeConflict, "session-memory ingress lease is not owned by this worker", err)
	}
	return nil
}

func validateNewIngressRecord(record sessionmemorycmd.IngressRecord) error {
	if record.SchemaVersion != sessionmemorycmd.IngressSchemaVersionV1 || record.ScopeSequence != 0 || record.State != sessionmemorycmd.IngressStatePending || record.Attempts != 0 || record.LeaseOwner != "" || record.LeaseUntil != nil || record.PublishedAt != nil || record.LastError != "" || record.CreatedAt.IsZero() || record.UpdatedAt.IsZero() {
		return sessionmemory.PermanentError(sessionmemory.CodePermanent, "new session-memory ingress record is invalid", nil)
	}
	return record.Export.Validate()
}

func loadIngressRecord(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, exportID string) (sessionmemorycmd.IngressRecord, bool, error) {
	row := queryer.QueryRowContext(ctx, `SELECT export_id, scope_key, scope_kind, scope_sequence, envelope_json, state, attempts, lease_owner, lease_until, last_error, created_at, updated_at, published_at FROM session_memory_ingress_outbox WHERE export_id = ?`, exportID)
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
	var exportID, scopeKey, scopeKind, envelope, state, leaseOwner, leaseUntil, lastError, createdAt, updatedAt, publishedAt string
	var sequence uint64
	var attempts uint32
	if err := scan(&exportID, &scopeKey, &scopeKind, &sequence, &envelope, &state, &attempts, &leaseOwner, &leaseUntil, &lastError, &createdAt, &updatedAt, &publishedAt); err != nil {
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
