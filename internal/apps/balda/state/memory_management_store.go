package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlMemoryManagementStore struct{ users *sqlUserStore }

var _ MemoryManagementStore = (*sqlMemoryManagementStore)(nil)

func (s *sqlMemoryManagementStore) CheckAuthority(ctx context.Context, a memorycmd.Authority) error {
	if err := validateMemoryAuthority(a); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return memorycmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.users.checkBrowserAuthority(ctx, tx, memoryBrowserAuthority(a)); err != nil {
		return memoryAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return memorycmd.ErrUnavailable
	}
	return nil
}

func (s *sqlMemoryManagementStore) Commit(ctx context.Context, m memorycmd.Mutation) error {
	if err := validateMemoryMutation(m); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return memorycmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	a := memoryBrowserAuthority(m.Authority)
	authority, err := s.users.checkBrowserAuthority(ctx, tx, a)
	if err != nil {
		return memoryAuthorityError(err)
	}
	var raw string
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace = ? AND key = ?`)+s.users.forUpdate, NamespaceApp, memorycmd.Key).Scan(&raw)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return memorycmd.ErrUnavailable
	}
	var baseline memorycmd.Record
	if found {
		baseline, err = decodeMemoryRecord([]byte(raw))
		if err != nil {
			return err
		}
	}
	if baseline.Version != m.ExpectedVersion {
		return memorycmd.ErrConflict
	}
	if baseline.UpdatedAt != "" {
		previous, _ := time.Parse(time.RFC3339Nano, baseline.UpdatedAt)
		current, _ := time.Parse(time.RFC3339Nano, m.Record.UpdatedAt)
		if !current.After(previous) {
			return memorycmd.ErrInvalid
		}
	}
	if err := authority.checkTime(a); err != nil {
		return memoryAuthorityError(err)
	}
	data, err := json.Marshal(m.Record)
	if err != nil {
		return memorycmd.ErrInvalid
	}
	var result sql.Result
	if found {
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_app_kv SET value_json = ?, expires_at = NULL, updated_at = ? WHERE namespace = ? AND key = ? AND value_json = ?`), string(data), m.Record.UpdatedAt, NamespaceApp, memorycmd.Key, raw)
	} else {
		result, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_app_kv (namespace, key, value_json, expires_at, updated_at) VALUES (?, ?, ?, NULL, ?) ON CONFLICT(namespace, key) DO NOTHING`), NamespaceApp, memorycmd.Key, string(data), m.Record.UpdatedAt)
	}
	if err != nil {
		return memorycmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return memorycmd.ErrUnavailable
	}
	if count != 1 {
		return memorycmd.ErrConflict
	}
	// Rebuild the envelope: request metadata and free-form reasons may contain facts.
	audit := usercmd.AuditEvent{ID: m.Audit.ID, Action: m.Audit.Action, Outcome: usercmd.AuditOutcomeSucceeded,
		ActorUserID: m.Authority.UserID, ActorSessionID: m.Authority.SessionID,
		TargetType: usercmd.AuditTargetMemoryFact, TargetID: m.Audit.TargetID, Source: "backoffice", OccurredAt: m.Authority.At}
	switch audit.Action {
	case usercmd.AuditActionMemoryFactCreated:
		audit.Reason = "Fact created"
	case usercmd.AuditActionMemoryFactEdited:
		audit.Reason = "Fact edited"
	case usercmd.AuditActionMemoryFactDeleted:
		audit.Reason = "Fact deleted"
	}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return memorycmd.ErrUnavailable
	}
	if err := authority.checkTime(a); err != nil {
		return memoryAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return memorycmd.ErrUnavailable
	}
	return nil
}

func validateMemoryAuthority(a memorycmd.Authority) error {
	if strings.TrimSpace(a.UserID) == "" || a.UserVersion == 0 || a.CredentialVersion == 0 || strings.TrimSpace(a.SessionID) == "" || a.SessionVersion == 0 || a.At.IsZero() {
		return memorycmd.ErrInvalid
	}
	return nil
}

func validateMemoryMutation(m memorycmd.Mutation) error {
	if err := validateMemoryAuthority(m.Authority); err != nil {
		return err
	}
	if m.ExpectedVersion < 0 || m.ExpectedVersion == math.MaxInt64 || m.Record.Version != m.ExpectedVersion+1 || !validMemoryRecord(m.Record) {
		return memorycmd.ErrInvalid
	}
	id, err := strconv.ParseInt(m.Audit.TargetID, 10, 64)
	if err != nil || id <= 0 || strconv.FormatInt(id, 10) != m.Audit.TargetID || id > m.Record.Version {
		return memorycmd.ErrInvalid
	}
	switch m.Audit.Action {
	case usercmd.AuditActionMemoryFactCreated, usercmd.AuditActionMemoryFactEdited, usercmd.AuditActionMemoryFactDeleted:
	default:
		return memorycmd.ErrInvalid
	}
	if usercmd.ValidateAuditEvent(m.Audit) != nil || m.Audit.TargetType != usercmd.AuditTargetMemoryFact ||
		m.Audit.ActorUserID != m.Authority.UserID || m.Audit.ActorSessionID != m.Authority.SessionID ||
		m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !m.Audit.OccurredAt.Equal(m.Authority.At) {
		return memorycmd.ErrInvalid
	}
	return nil
}

func validMemoryRecord(rec memorycmd.Record) bool {
	if rec.Version < 0 {
		return false
	}
	if rec.Version == 0 {
		return rec.UpdatedAt == "" && len(rec.Entries) == 0
	}
	updated, err := time.Parse(time.RFC3339Nano, rec.UpdatedAt)
	if err != nil || updated.IsZero() {
		return false
	}
	ids := make(map[int64]bool, len(rec.Entries))
	for _, entry := range rec.Entries {
		created, err := time.Parse(time.RFC3339Nano, entry.CreatedAt)
		if entry.Version <= 0 || entry.Version > rec.Version || ids[entry.Version] || strings.TrimSpace(entry.Fact) == "" || err != nil || created.IsZero() || created.After(updated) {
			return false
		}
		ids[entry.Version] = true
	}
	return true
}

func decodeMemoryRecord(data []byte) (memorycmd.Record, error) {
	var rec *memorycmd.Record
	if err := json.Unmarshal(data, &rec); err != nil || rec == nil || !validMemoryRecord(*rec) {
		return memorycmd.Record{}, memorycmd.ErrUnavailable
	}
	return *rec, nil
}

func memoryBrowserAuthority(a memorycmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID, userVersion: a.UserVersion,
		credentialVersion: a.CredentialVersion, mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func memoryAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return memorycmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return memorycmd.ErrForbidden
	default:
		return memorycmd.ErrUnavailable
	}
}
