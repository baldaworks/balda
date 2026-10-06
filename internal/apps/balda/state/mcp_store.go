package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlMCPStore struct {
	users *sqlUserStore
}

var _ MCPStore = (*sqlMCPStore)(nil)

const mcpConnectionColumns = `connection_id, public_id, source, current_revision_id,
	enabled, deleted, version, published_version, created_at, updated_at`

// CheckMCPAuthority is a read-only preflight for privileged external side effects.
func (s *sqlMCPStore) CheckMCPAuthority(ctx context.Context, a mcpcmd.Authority) error {
	if err := validateMCPAuthority(a); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return mcpStoreError("begin MCP authority check", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.checkAuthority(ctx, tx, a); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return mcpMutationError(err)
	}
	return nil
}

func (s *sqlMCPStore) SaveMCPConnection(ctx context.Context, m MCPMutation) error {
	if err := validateMCPMutation(m); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return mcpStoreError("begin MCP mutation", err)
	}
	defer func() { _ = tx.Rollback() }()
	authority, err := s.checkAuthority(ctx, tx, m.Authority)
	if err != nil {
		return err
	}
	c := m.Connection
	current, found, err := s.connection(ctx, tx, c.ID, true)
	if err != nil {
		return err
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if m.ExpectedVersion == 0 {
		if found {
			return mcpcmd.ErrConflict
		}
		_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_mcp_connections (
			`+mcpConnectionColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`),
			c.ID, c.PublicID, c.Source, c.CurrentRevisionID, boolInt(c.Enabled), boolInt(c.Deleted),
			1, 0, formatUserTime(c.CreatedAt), formatUserTime(c.UpdatedAt))
	} else {
		if !found {
			return mcpcmd.ErrNotFound
		}
		if current.Version != m.ExpectedVersion || current.Source != c.Source || current.PublicID != c.PublicID || current.Deleted {
			return mcpcmd.ErrConflict
		}
		if m.Revision == nil && c.CurrentRevisionID != current.CurrentRevisionID {
			return mcpcmd.ErrInvalid
		}
		if c.UpdatedAt.Before(current.UpdatedAt) {
			return mcpcmd.ErrInvalid
		}
		_, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_mcp_connections SET
			current_revision_id = ?, enabled = ?, deleted = ?, version = ?, updated_at = ?
			WHERE connection_id = ? AND version = ?`),
			c.CurrentRevisionID, boolInt(c.Enabled), boolInt(c.Deleted), m.ExpectedVersion+1,
			formatUserTime(c.UpdatedAt), c.ID, m.ExpectedVersion)
	}
	if err != nil {
		return mcpMutationError(err)
	}
	if m.Revision != nil {
		r := m.Revision
		definition, err := json.Marshal(r.Definition)
		if err != nil {
			return mcpcmd.ErrInvalid
		}
		protected := r.ProtectedValues
		if protected == nil {
			protected = []byte{}
		}
		_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_mcp_revisions
			(connection_id, revision_id, definition_json, protected_values, created_at)
			VALUES (?, ?, ?, ?, ?)`), r.ConnectionID, r.ID, string(definition), protected, formatUserTime(r.CreatedAt))
		if err != nil {
			return mcpMutationError(err)
		}
	}
	// Metadata and reasons are server-owned. Definition input, credentials and
	// transport failures must never be copied into the immutable security audit.
	audit := m.Audit
	audit.Reason = "MCP definition or selection changed"
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return mcpMutationError(err)
	}
	if err := authority.checkTime(m.Authority); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return mcpMutationError(err)
	}
	return nil
}

func validateMCPMutation(m MCPMutation) error {
	c, a := m.Connection, m.Authority
	if c.ID == "" || len(c.ID) > 256 || c.PublicID == "" || len(c.PublicID) > 256 ||
		(c.Source != mcpcmd.SourceManaged && c.Source != mcpcmd.SourceConfig) ||
		c.CurrentRevisionID == "" || len(c.CurrentRevisionID) > 256 ||
		c.CreatedAt.IsZero() || c.UpdatedAt.Before(c.CreatedAt) ||
		m.ExpectedVersion >= math.MaxInt64 || (c.Deleted && c.Enabled) ||
		(m.ExpectedVersion == 0 && (m.Revision == nil || c.Deleted)) {
		return mcpcmd.ErrInvalid
	}
	if err := validateMCPAuthority(a); err != nil {
		return err
	}
	if err := usercmd.ValidateAuditEvent(m.Audit); err != nil ||
		m.Audit.Action != usercmd.AuditActionMCPDefinitionChanged || m.Audit.TargetType != usercmd.AuditTargetMCP ||
		m.Audit.TargetID != c.ID || m.Audit.ActorUserID != a.UserID || m.Audit.ActorSessionID != a.SessionID ||
		m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || !m.Audit.OccurredAt.Equal(a.At) {
		return mcpcmd.ErrInvalid
	}
	if r := m.Revision; r != nil {
		if r.ConnectionID != c.ID || r.ID != c.CurrentRevisionID || r.CreatedAt.IsZero() {
			return mcpcmd.ErrInvalid
		}
		for _, bindings := range []map[string]mcpcmd.ValueBinding{r.Definition.Env, r.Definition.Headers} {
			for _, binding := range bindings {
				if binding.Kind == mcpcmd.ValueProtected && binding.Value != "" {
					return mcpcmd.ErrInvalid
				}
			}
		}
	}
	return nil
}

func validateMCPAuthority(a mcpcmd.Authority) error {
	if a.UserID == "" || a.UserVersion == 0 || a.CredentialVersion == 0 || a.SessionID == "" || a.SessionVersion == 0 || a.At.IsZero() {
		return mcpcmd.ErrInvalid
	}
	return nil
}

// Canonical user -> browser family is the same lock order as security writes.
// This closes the gap between an HTTP guard and commit when access is revoked.
func (s *sqlMCPStore) checkAuthority(ctx context.Context, tx *sql.Tx, a mcpcmd.Authority) (lockedMCPAuthority, error) {
	profile, err := s.users.mfaAuthority(ctx, tx, a.UserID, a.UserVersion, a.CredentialVersion, a.MFAVersion)
	if err != nil {
		return lockedMCPAuthority{}, mcpMutationError(err)
	}
	family, err := s.users.liveMFASession(ctx, tx, a.SessionID, a.UserID, a.CredentialVersion, a.At)
	if err != nil {
		return lockedMCPAuthority{}, mcpMutationError(err)
	}
	if family.Assurance != usercmd.SessionAssuranceNormal {
		return lockedMCPAuthority{}, mcpcmd.ErrForbidden
	}
	if family.Version != a.SessionVersion {
		return lockedMCPAuthority{}, mcpcmd.ErrConflict
	}
	if profile.Enabled && family.MFAFactorID != profile.Credential.ID {
		return lockedMCPAuthority{}, mcpcmd.ErrForbidden
	}
	authority := lockedMCPAuthority{family: family, mfaEnabled: profile.Enabled}
	return authority, authority.checkTime(a)
}

// The canonical user and family stay locked while target rows are acquired.
// Recheck time after those waits without changing audit or metadata timestamps.
type lockedMCPAuthority struct {
	family     usercmd.SessionFamily
	mfaEnabled bool
}

func (f lockedMCPAuthority) checkTime(a mcpcmd.Authority) error {
	// Caller time remains a conservative guard, never an upper freshness bound.
	for _, now := range []time.Time{time.Now(), a.At} {
		if !now.Before(f.family.Access.ExpiresAt) || !now.Before(f.family.RefreshExpiresAt) {
			return mcpcmd.ErrForbidden
		}
		if f.mfaEnabled && (f.family.WebAuthnVerifiedAt.IsZero() || now.Before(f.family.WebAuthnVerifiedAt)) {
			return mcpcmd.ErrForbidden
		}
	}
	return nil
}

func (s *sqlMCPStore) GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error) {
	return s.connection(ctx, s.users.db, id, false)
}

func (s *sqlMCPStore) connection(ctx context.Context, q userQueryer, id string, lock bool) (mcpcmd.Connection, bool, error) {
	query := s.users.bind(`SELECT ` + mcpConnectionColumns + ` FROM balda_mcp_connections WHERE connection_id = ?`)
	if lock {
		query += s.users.forUpdate
	}
	c, err := scanMCPConnection(q.QueryRowContext(ctx, query, id))
	if errors.Is(err, sql.ErrNoRows) {
		return mcpcmd.Connection{}, false, nil
	}
	if err != nil {
		return mcpcmd.Connection{}, false, mcpStoreError("read MCP connection", err)
	}
	return c, true, nil
}

func scanMCPConnection(row interface{ Scan(dest ...any) error }) (mcpcmd.Connection, error) {
	var c mcpcmd.Connection
	var enabled, deleted int
	var created, updated string
	err := row.Scan(&c.ID, &c.PublicID, &c.Source, &c.CurrentRevisionID, &enabled, &deleted,
		&c.Version, &c.PublishedVersion, &created, &updated)
	if err != nil {
		return c, err
	}
	c.Enabled, c.Deleted = intBool(enabled), intBool(deleted)
	if c.CreatedAt, err = parseUserTime(created); err != nil {
		return c, err
	}
	c.UpdatedAt, err = parseUserTime(updated)
	return c, err
}

func (s *sqlMCPStore) ListMCPConnections(ctx context.Context) ([]mcpcmd.Connection, error) {
	rows, err := s.users.db.QueryContext(ctx, `SELECT `+mcpConnectionColumns+` FROM balda_mcp_connections ORDER BY public_id, connection_id`)
	if err != nil {
		return nil, mcpStoreError("list MCP connections", err)
	}
	defer func() { _ = rows.Close() }()
	var connections []mcpcmd.Connection
	for rows.Next() {
		c, err := scanMCPConnection(rows)
		if err != nil {
			return nil, mcpStoreError("read MCP connections", err)
		}
		connections = append(connections, c)
	}
	if err := rows.Err(); err != nil {
		return nil, mcpStoreError("iterate MCP connections", err)
	}
	return connections, nil
}

func (s *sqlMCPStore) GetMCPRevision(ctx context.Context, connectionID, revisionID string) (mcpcmd.Revision, bool, error) {
	row := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT connection_id, revision_id, definition_json,
		protected_values, created_at FROM balda_mcp_revisions WHERE connection_id = ? AND revision_id = ?`), connectionID, revisionID)
	r, err := scanMCPRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpcmd.Revision{}, false, nil
	}
	if err != nil {
		return mcpcmd.Revision{}, false, mcpStoreError("read MCP revision", err)
	}
	return r, true, nil
}

func scanMCPRevision(row interface{ Scan(dest ...any) error }) (mcpcmd.Revision, error) {
	var r mcpcmd.Revision
	var definition, created string
	err := row.Scan(&r.ConnectionID, &r.ID, &definition, &r.ProtectedValues, &created)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(definition), &r.Definition); err != nil {
		return mcpcmd.Revision{}, err
	}
	r.CreatedAt, err = parseUserTime(created)
	return r, err
}

func (s *sqlMCPStore) ListMCPRevisions(ctx context.Context) ([]mcpcmd.Revision, error) {
	rows, err := s.users.db.QueryContext(ctx, `SELECT connection_id, revision_id, definition_json,
		protected_values, created_at FROM balda_mcp_revisions ORDER BY connection_id, revision_id`)
	if err != nil {
		return nil, mcpStoreError("list MCP revisions", err)
	}
	defer func() { _ = rows.Close() }()
	var revisions []mcpcmd.Revision
	for rows.Next() {
		r, err := scanMCPRevision(rows)
		if err != nil {
			return nil, mcpStoreError("read MCP revisions", err)
		}
		revisions = append(revisions, r)
	}
	if err := rows.Err(); err != nil {
		return nil, mcpStoreError("iterate MCP revisions", err)
	}
	return revisions, nil
}

func (s *sqlMCPStore) MarkMCPPublished(ctx context.Context, id string, version uint64) error {
	if version == 0 || version > math.MaxInt64 {
		return mcpcmd.ErrInvalid
	}
	result, err := s.users.db.ExecContext(ctx, s.users.bind(`UPDATE balda_mcp_connections SET published_version = ?
		WHERE connection_id = ? AND version = ? AND published_version <= ?`), version, id, version, version)
	if err != nil {
		return mcpMutationError(err)
	}
	n, err := result.RowsAffected()
	if err != nil {
		return mcpStoreError("check MCP publication", err)
	}
	if n != 1 {
		return mcpcmd.ErrConflict
	}
	return nil
}

func mcpMutationError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return mcpcmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return mcpcmd.ErrForbidden
	case errors.Is(err, usercmd.ErrInvalid):
		return mcpcmd.ErrInvalid
	}
	var sqlState interface{ SQLState() string }
	if errors.As(err, &sqlState) && len(sqlState.SQLState()) == 5 && sqlState.SQLState()[:2] == "23" {
		return mcpcmd.ErrConflict
	}
	// SQLite reports typed numeric extended constraint codes.
	var coded interface{ Code() int }
	if errors.As(err, &coded) && coded.Code()&0xff == 19 {
		return mcpcmd.ErrConflict
	}
	return mcpStoreError("write MCP state", err)
}

// Database messages may contain submitted values or trigger diagnostics. Expose
// only the operation, preserving cancellation for callers without leaking data.
func mcpStoreError(operation string, err error) error {
	if errors.Is(err, context.Canceled) {
		return fmt.Errorf("%s: %w", operation, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%s: %w", operation, context.DeadlineExceeded)
	}
	return fmt.Errorf("%s failed", operation)
}
