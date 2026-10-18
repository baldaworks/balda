package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// SkillManagementStore exposes guarded standalone metadata storage. Applications
// consume their own structurally equivalent port; state imports no policy owner.
type SkillManagementStore interface {
	CheckAuthority(ctx context.Context, authority skillcmd.Authority) error
	Observe(ctx context.Context) (skillcmd.Observation, error)
	Commit(ctx context.Context, mutation skillcmd.Commit) error
	ReadSelections(ctx context.Context) (runtimecatalogcmd.ManagedSkillSelection, error)
	MarkPublished(ctx context.Context, evidence runtimecatalogcmd.ManagedSkillSelection) error
}

type sqlSkillManagementStore struct{ users *sqlUserStore }

func (s *sqlSkillManagementStore) CheckAuthority(ctx context.Context, a skillcmd.Authority) error {
	if !a.Valid() {
		return skillcmd.ErrInvalid
	}
	if a.CLI() {
		return nil
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return skillcmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.users.checkBrowserAuthority(ctx, tx, skillBrowserAuthority(a)); err != nil {
		return skillAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return skillcmd.ErrUnavailable
	}
	return nil
}

func (s *sqlSkillManagementStore) Observe(ctx context.Context) (skillcmd.Observation, error) {
	var raw string
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace = ? AND key = ?`), NamespaceApp, skillcmd.ManagementKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return skillcmd.Observation{}, nil
	}
	if err != nil {
		return skillcmd.Observation{}, skillcmd.ErrUnavailable
	}
	return decodeSkillRecord(raw)
}

func decodeSkillRecord(raw string) (skillcmd.Observation, error) {
	var record *skillcmd.Record
	if err := json.Unmarshal([]byte(raw), &record); err != nil || record == nil || record.Validate() != nil {
		return skillcmd.Observation{}, skillcmd.ErrUnavailable
	}
	return skillcmd.Observation{Record: *record}, nil
}

func (s *sqlSkillManagementStore) ReadSelections(ctx context.Context) (runtimecatalogcmd.ManagedSkillSelection, error) {
	o, err := s.Observe(ctx)
	if err != nil {
		return runtimecatalogcmd.ManagedSkillSelection{}, err
	}
	return o.Record.Selection(), nil
}

func (s *sqlSkillManagementStore) lockedRecord(ctx context.Context, tx *sql.Tx) (skillcmd.Observation, string, error) {
	var raw string
	err := tx.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace = ? AND key = ?`)+s.users.forUpdate, NamespaceApp, skillcmd.ManagementKey).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return skillcmd.Observation{}, "", nil
	}
	if err != nil {
		return skillcmd.Observation{}, "", skillcmd.ErrUnavailable
	}
	o, err := decodeSkillRecord(raw)
	return o, raw, err
}

func (s *sqlSkillManagementStore) Commit(ctx context.Context, m skillcmd.Commit) error {
	if m.Validate() != nil {
		return skillcmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return skillcmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := s.checkSkillAuthority(ctx, tx, m.Authority)
	if err != nil {
		return err
	}
	baseline, raw, err := s.lockedRecord(ctx, tx)
	if err != nil {
		return err
	}
	if baseline.Token() != m.ExpectedState {
		return skillcmd.ErrConflict
	}
	if m.Record.Generation != baseline.Record.Generation+1 || !preservesSkillTombstones(baseline.Record, m.Record) {
		return skillcmd.ErrInvalid
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if err := s.replace(ctx, tx, raw, m.Record); err != nil {
		return err
	}
	// Reconstruct the entire envelope: no source, description or arbitrary audit text.
	targetID := m.TargetID
	if m.Operation == skillcmd.OperationRefresh {
		targetID = "skills"
	}
	audit := usercmd.AuditEvent{ID: m.AuditID, Action: usercmd.AuditActionSkillChanged, Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.actorUserID, ActorSessionID: authority.actorSessionID, TargetType: usercmd.AuditTargetSkill, TargetID: targetID, Source: authority.source, Reason: skillAuditReason(m), OccurredAt: m.Authority.At}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return skillcmd.ErrUnavailable
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return skillcmd.ErrUnavailable
	}
	return nil
}

// Record exact accepted evidence from protected metadata, never browser audit text.
func skillAuditReason(m skillcmd.Commit) string {
	selection := m.Record.Selection()
	data, _ := json.Marshal(selection.Sources)
	reason := fmt.Sprintf("Standalone skill %s; generation=%d; selection=%x", m.Operation, m.Record.Generation, sha256.Sum256(data))
	if m.Operation != skillcmd.OperationRefresh {
		for _, i := range m.Record.Installations {
			if i.Descriptor.ID.Name == m.TargetID {
				return fmt.Sprintf("%s; revision_sha256=%x", reason, sha256.Sum256([]byte(i.Descriptor.Revision)))
			}
		}
	}
	return reason
}

func preservesSkillTombstones(before, after skillcmd.Record) bool {
	next := make(map[runtimecatalogcmd.SourceID]skillcmd.Installation, len(after.Installations))
	for _, i := range after.Installations {
		next[i.Descriptor.ID] = i
	}
	for _, i := range before.Installations {
		n, ok := next[i.Descriptor.ID]
		if !ok || !i.CreatedAt.Equal(n.CreatedAt) || (i.Removed && !n.Removed) {
			return false
		}
	}
	return true
}

func (s *sqlSkillManagementStore) replace(ctx context.Context, tx *sql.Tx, raw string, record skillcmd.Record) error {
	data, err := json.Marshal(record)
	if err != nil {
		return skillcmd.ErrInvalid
	}
	var result sql.Result
	at := record.UpdatedAt.UTC().Format(time.RFC3339Nano)
	if raw == "" {
		result, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_app_kv (namespace,key,value_json,expires_at,updated_at) VALUES (?,?,?,NULL,?) ON CONFLICT(namespace,key) DO NOTHING`), NamespaceApp, skillcmd.ManagementKey, string(data), at)
	} else {
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_app_kv SET value_json = ?, expires_at = NULL, updated_at = ? WHERE namespace = ? AND key = ? AND value_json = ?`), string(data), at, NamespaceApp, skillcmd.ManagementKey, raw)
	}
	if err != nil {
		return skillcmd.ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return skillcmd.ErrUnavailable
	}
	if n != 1 {
		return skillcmd.ErrConflict
	}
	return nil
}

func (s *sqlSkillManagementStore) MarkPublished(ctx context.Context, evidence runtimecatalogcmd.ManagedSkillSelection) error {
	tx, err := s.users.begin(ctx)
	if err != nil {
		return skillcmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	o, raw, err := s.lockedRecord(ctx, tx)
	if err != nil {
		return err
	}
	if !o.Record.Selection().Equal(evidence) {
		return skillcmd.ErrConflict
	}
	if o.Record.Pending {
		o.Record.Pending = false
		if err := s.replace(ctx, tx, raw, o.Record); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return skillcmd.ErrUnavailable
	}
	return nil
}

func skillBrowserAuthority(a skillcmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID, userVersion: a.UserVersion, credentialVersion: a.CredentialVersion, mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

type checkedSkillAuthority struct {
	browser        *lockedBrowserAuthority
	actorUserID    string
	actorSessionID string
	source         string
}

func (s *sqlSkillManagementStore) checkSkillAuthority(ctx context.Context, tx *sql.Tx, a skillcmd.Authority) (checkedSkillAuthority, error) {
	if !a.Valid() {
		return checkedSkillAuthority{}, skillcmd.ErrInvalid
	}
	if a.CLI() {
		return checkedSkillAuthority{source: "cli"}, nil
	}
	if !a.Browser() {
		return checkedSkillAuthority{}, skillcmd.ErrInvalid
	}
	locked, err := s.users.checkBrowserAuthority(ctx, tx, skillBrowserAuthority(a))
	if err != nil {
		return checkedSkillAuthority{}, skillAuthorityError(err)
	}
	return checkedSkillAuthority{browser: &locked, actorUserID: a.UserID, actorSessionID: a.SessionID, source: "backoffice"}, nil
}

func (a checkedSkillAuthority) checkTime(request skillcmd.Authority) error {
	if a.browser == nil {
		return nil
	}
	if err := a.browser.checkTime(skillBrowserAuthority(request)); err != nil {
		return skillAuthorityError(err)
	}
	return nil
}

func skillAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return skillcmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return skillcmd.ErrForbidden
	default:
		return skillcmd.ErrUnavailable
	}
}
