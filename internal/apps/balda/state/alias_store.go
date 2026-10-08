package state

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlAliasStore struct {
	users *sqlUserStore
}

var _ AliasStore = (*sqlAliasStore)(nil)

func (s *sqlAliasStore) CheckAuthority(ctx context.Context, a aliascmd.Authority) error {
	if err := validateAliasAuthority(a); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return aliascmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.checkAuthority(ctx, tx, a); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return aliascmd.ErrUnavailable
	}
	return nil
}

func (s *sqlAliasStore) Get(ctx context.Context, name string) (aliascmd.Record, bool, error) {
	record, err := scanAlias(s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT name, locator_ref, version, created_at, updated_at
		FROM balda_managed_aliases WHERE name = ? AND deleted = 0`), name))
	if errors.Is(err, sql.ErrNoRows) {
		return aliascmd.Record{}, false, nil
	}
	if err != nil {
		return aliascmd.Record{}, false, aliascmd.ErrUnavailable
	}
	return record, true, nil
}

func (s *sqlAliasStore) List(ctx context.Context) ([]aliascmd.Record, error) {
	rows, err := s.users.db.QueryContext(ctx, `SELECT name, locator_ref, version, created_at, updated_at
		FROM balda_managed_aliases WHERE deleted = 0 ORDER BY name`)
	if err != nil {
		return nil, aliascmd.ErrUnavailable
	}
	defer func() { _ = rows.Close() }()
	var records []aliascmd.Record
	for rows.Next() {
		record, err := scanAlias(rows)
		if err != nil {
			return nil, aliascmd.ErrUnavailable
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, aliascmd.ErrUnavailable
	}
	return records, nil
}

func scanAlias(row interface{ Scan(dest ...any) error }) (aliascmd.Record, error) {
	var record aliascmd.Record
	var created, updated string
	if err := row.Scan(&record.Name, &record.LocatorRef, &record.Version, &created, &updated); err != nil {
		return aliascmd.Record{}, err
	}
	var err error
	record.CreatedAt, err = parseUserTime(created)
	if err != nil {
		return aliascmd.Record{}, err
	}
	record.UpdatedAt, err = parseUserTime(updated)
	return record, err
}

func (s *sqlAliasStore) Save(ctx context.Context, m aliascmd.Mutation) error {
	if err := validateAliasMutation(m); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return aliascmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := s.checkAuthority(ctx, tx, m.Authority)
	if err != nil {
		return err
	}
	if err := authority.checkTime(aliasBrowserAuthority(m.Authority)); err != nil {
		return aliasAuthorityError(err)
	}
	now := formatUserTime(time.Now().UTC())
	var result sql.Result
	switch m.Kind {
	case aliascmd.MutationCreate:
		result, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_managed_aliases
			(name, locator_ref, version, deleted, created_at, updated_at)
			VALUES (?, ?, 1, 0, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				locator_ref = excluded.locator_ref,
				version = balda_managed_aliases.version + 1,
				deleted = 0,
				created_at = excluded.created_at,
				updated_at = excluded.updated_at
			WHERE balda_managed_aliases.deleted = 1`), m.Record.Name, m.Record.LocatorRef, now, now)
	case aliascmd.MutationRetarget:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_managed_aliases
			SET locator_ref = ?, version = version + 1, updated_at = ?
			WHERE name = ? AND version = ? AND deleted = 0`), m.Record.LocatorRef, now, m.Record.Name, m.ExpectedVersion)
	case aliascmd.MutationDelete:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_managed_aliases
			SET deleted = 1, version = version + 1, updated_at = ?
			WHERE name = ? AND version = ? AND deleted = 0`), now, m.Record.Name, m.ExpectedVersion)
	}
	if err != nil {
		return aliascmd.ErrUnavailable
	}
	count, err := result.RowsAffected()
	if err != nil {
		return aliascmd.ErrUnavailable
	}
	if count != 1 {
		return aliascmd.ErrConflict
	}
	audit := m.Audit
	switch m.Kind {
	case aliascmd.MutationCreate:
		audit.Reason = "Alias created"
	case aliascmd.MutationRetarget:
		audit.Reason = "Alias retargeted"
	case aliascmd.MutationDelete:
		audit.Reason = "Alias deleted"
	}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return aliascmd.ErrUnavailable
	}
	if err := authority.checkTime(aliasBrowserAuthority(m.Authority)); err != nil {
		return aliasAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return aliascmd.ErrUnavailable
	}
	return nil
}

func validateAliasMutation(m aliascmd.Mutation) error {
	a, r := m.Authority, m.Record
	if err := validateAliasAuthority(a); err != nil {
		return err
	}
	if strings.TrimSpace(r.Name) == "" || (m.Kind != aliascmd.MutationDelete && strings.TrimSpace(r.LocatorRef) == "") ||
		m.ExpectedVersion >= math.MaxInt64 || r.Version != m.ExpectedVersion+1 ||
		(m.Kind == aliascmd.MutationCreate && m.ExpectedVersion != 0) ||
		(m.Kind != aliascmd.MutationCreate && m.ExpectedVersion == 0) ||
		(m.Kind != aliascmd.MutationCreate && m.Kind != aliascmd.MutationRetarget && m.Kind != aliascmd.MutationDelete) {
		return aliascmd.ErrInvalid
	}
	action := map[aliascmd.MutationKind]usercmd.AuditAction{
		aliascmd.MutationCreate:   usercmd.AuditActionAliasCreated,
		aliascmd.MutationRetarget: usercmd.AuditActionAliasRetargeted,
		aliascmd.MutationDelete:   usercmd.AuditActionAliasDeleted,
	}[m.Kind]
	if err := usercmd.ValidateAuditEvent(m.Audit); err != nil ||
		m.Audit.Action != action || m.Audit.TargetType != usercmd.AuditTargetAlias ||
		m.Audit.TargetID != r.Name || m.Audit.ActorUserID != a.UserID || m.Audit.ActorSessionID != a.SessionID ||
		m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !m.Audit.OccurredAt.Equal(a.At) {
		return aliascmd.ErrInvalid
	}
	return nil
}

func validateAliasAuthority(a aliascmd.Authority) error {
	if a.UserID == "" || a.UserVersion == 0 || a.CredentialVersion == 0 ||
		a.SessionID == "" || a.SessionVersion == 0 || a.At.IsZero() {
		return aliascmd.ErrInvalid
	}
	return nil
}

func (s *sqlAliasStore) checkAuthority(ctx context.Context, tx *sql.Tx, a aliascmd.Authority) (lockedBrowserAuthority, error) {
	locked, err := s.users.checkBrowserAuthority(ctx, tx, aliasBrowserAuthority(a))
	if err != nil {
		return lockedBrowserAuthority{}, aliasAuthorityError(err)
	}
	return locked, nil
}

func aliasBrowserAuthority(a aliascmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID,
		userVersion: a.UserVersion, credentialVersion: a.CredentialVersion,
		mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func aliasAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return aliascmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return aliascmd.ErrForbidden
	default:
		return aliascmd.ErrUnavailable
	}
}
