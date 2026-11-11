package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// HookManagementStore is the storage port for runtime reads and guarded browser writes.
type HookManagementStore interface {
	Load(ctx context.Context) (hookcmd.Record, error)
	CheckAuthority(ctx context.Context, authority hookcmd.Authority) error
	Commit(ctx context.Context, mutation hookcmd.Mutation) error
}

type sqlHookManagementStore struct{ users *sqlUserStore }

func (s *sqlHookManagementStore) Load(ctx context.Context) (hookcmd.Record, error) {
	var raw string
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace = ? AND key = ?`), NamespaceApp, hookcmd.Key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return hookcmd.Record{}, nil
	}
	if err != nil {
		return hookcmd.Record{}, hookcmd.ErrUnavailable
	}
	return decodeHookRecord(raw)
}

func (s *sqlHookManagementStore) CheckAuthority(ctx context.Context, a hookcmd.Authority) error {
	if !validHookAuthority(a) {
		return hookcmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.users.checkBrowserAuthority(ctx, tx, hookBrowserAuthority(a)); err != nil {
		return hookAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return hookcmd.ErrUnavailable
	}
	return nil
}

func (s *sqlHookManagementStore) Commit(ctx context.Context, m hookcmd.Mutation) error {
	if m.Validate() != nil {
		return hookcmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	a := hookBrowserAuthority(m.Authority)
	authority, err := s.users.checkBrowserAuthority(ctx, tx, a)
	if err != nil {
		return hookAuthorityError(err)
	}
	var raw string
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace = ? AND key = ?`)+s.users.forUpdate, NamespaceApp, hookcmd.Key).Scan(&raw)
	found := err == nil
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return hookcmd.ErrUnavailable
	}
	var baseline hookcmd.Record
	if found {
		baseline, err = decodeHookRecord(raw)
		if err != nil {
			return err
		}
	}
	if baseline.Version != m.ExpectedVersion {
		return hookcmd.ErrConflict
	}
	if baseline.UpdatedAt != "" {
		previous, _ := time.Parse(time.RFC3339Nano, baseline.UpdatedAt)
		current, _ := time.Parse(time.RFC3339Nano, m.Record.UpdatedAt)
		if !current.After(previous) {
			return hookcmd.ErrInvalid
		}
	}
	if err := authority.checkTime(a); err != nil {
		return hookAuthorityError(err)
	}
	data, err := json.Marshal(m.Record)
	if err != nil {
		return hookcmd.ErrInvalid
	}
	var result sql.Result
	if found {
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_app_kv SET value_json = ?, expires_at = NULL, updated_at = ? WHERE namespace = ? AND key = ? AND value_json = ?`), string(data), m.Record.UpdatedAt, NamespaceApp, hookcmd.Key, raw)
	} else {
		result, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_app_kv (namespace, key, value_json, expires_at, updated_at) VALUES (?, ?, ?, NULL, ?) ON CONFLICT(namespace, key) DO NOTHING`), NamespaceApp, hookcmd.Key, string(data), m.Record.UpdatedAt)
	}
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return hookcmd.ErrUnavailable
	}
	if count != 1 {
		return hookcmd.ErrConflict
	}
	// Rebuild the envelope so executable arguments and event payloads never enter audit.
	audit := usercmd.AuditEvent{ID: m.Audit.ID, Action: usercmd.AuditActionHookChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: m.Authority.UserID, ActorSessionID: m.Authority.SessionID, TargetType: usercmd.AuditTargetHook, TargetID: m.Audit.TargetID, Source: "backoffice", Reason: "Agent hook definition changed", OccurredAt: m.Authority.At}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return hookcmd.ErrUnavailable
	}
	if err := authority.checkTime(a); err != nil {
		return hookAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return hookcmd.ErrUnavailable
	}
	return nil
}

func decodeHookRecord(raw string) (hookcmd.Record, error) {
	var record *hookcmd.Record
	if err := json.Unmarshal([]byte(raw), &record); err != nil || record == nil || record.Validate() != nil {
		return hookcmd.Record{}, hookcmd.ErrUnavailable
	}
	return *record, nil
}

func validHookAuthority(a hookcmd.Authority) bool {
	return a.UserID != "" && a.UserVersion != 0 && a.CredentialVersion != 0 && a.SessionID != "" && a.SessionVersion != 0 && !a.At.IsZero()
}

func hookBrowserAuthority(a hookcmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID, userVersion: a.UserVersion, credentialVersion: a.CredentialVersion, mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func hookAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return hookcmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return hookcmd.ErrForbidden
	default:
		return hookcmd.ErrUnavailable
	}
}
