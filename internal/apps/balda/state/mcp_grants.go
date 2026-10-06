package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const mcpGrantColumns = `grant_id, connection_id, resource, issuer, client_id,
	generation, status, scopes_json, token_endpoint_auth_method, client_secret_expires_at,
	access_expires_at, protected_values, created_at, updated_at`

func (s *sqlMCPStore) SaveMCPGrant(ctx context.Context, m MCPGrantMutation) error {
	if err := validateMCPGrantMutation(m); err != nil {
		return err
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return mcpStoreError("begin MCP grant mutation", err)
	}
	defer func() { _ = tx.Rollback() }()
	var authority lockedMCPAuthority
	if m.Authority != nil {
		authority, err = s.checkAuthority(ctx, tx, *m.Authority)
		if err != nil {
			return err
		}
	}
	g := m.Grant
	c, found, err := s.connection(ctx, tx, g.Binding.ConnectionID, true)
	if err != nil {
		return err
	}
	if !found {
		return mcpcmd.ErrNotFound
	}
	// Disable/delete only removes new selection. Retained pins may still renew
	// or explicitly disconnect their exact worker authorization context.
	if c.Deleted && (m.Operation == mcpcmd.GrantRegister || m.Operation == mcpcmd.GrantAuthorize) {
		return mcpcmd.ErrConflict
	}
	if m.Operation == mcpcmd.GrantAuthorize && c.CurrentRevisionID != m.ExpectedRevisionID {
		return mcpcmd.ErrConflict
	}
	current, found, err := s.grantByID(ctx, tx, g.ID, true)
	if err != nil {
		return err
	}
	if m.ExpectedGeneration == 0 {
		if found || m.Operation != mcpcmd.GrantRegister {
			return mcpcmd.ErrConflict
		}
	} else {
		if !found || current.Generation != m.ExpectedGeneration || current.Binding != g.Binding || !current.CreatedAt.Equal(g.CreatedAt) || g.UpdatedAt.Before(current.UpdatedAt) {
			return mcpcmd.ErrConflict
		}
		if (m.Operation == mcpcmd.GrantAuthorize && current.Status != mcpcmd.GrantAuthRequired) || (m.Operation == mcpcmd.GrantRenew && current.Status != mcpcmd.GrantAuthorized) {
			return mcpcmd.ErrConflict
		}
		if m.Operation != mcpcmd.GrantRegister && (g.TokenEndpointAuthMethod != current.TokenEndpointAuthMethod || !g.ClientSecretExpiresAt.Equal(current.ClientSecretExpiresAt)) {
			return mcpcmd.ErrConflict
		}
	}
	scopes, err := json.Marshal(g.Scopes)
	if err != nil {
		return mcpcmd.ErrInvalid
	}
	payload := g.ProtectedValues
	if payload == nil {
		payload = []byte{}
	}
	if m.Authority != nil {
		if err := authority.checkTime(*m.Authority); err != nil {
			return err
		}
	}
	if m.ExpectedGeneration == 0 {
		_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_mcp_grants (`+mcpGrantColumns+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`), g.ID, g.Binding.ConnectionID, g.Binding.Resource, g.Binding.Issuer, g.Binding.ClientID, g.Generation, g.Status, string(scopes), g.TokenEndpointAuthMethod, formatUserTime(g.ClientSecretExpiresAt), formatUserTime(g.AccessExpiresAt), payload, formatUserTime(g.CreatedAt), formatUserTime(g.UpdatedAt))
	} else {
		_, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_mcp_grants SET generation = ?, status = ?, scopes_json = ?, token_endpoint_auth_method = ?, client_secret_expires_at = ?, access_expires_at = ?, protected_values = ?, updated_at = ? WHERE grant_id = ? AND generation = ?`), g.Generation, g.Status, string(scopes), g.TokenEndpointAuthMethod, formatUserTime(g.ClientSecretExpiresAt), formatUserTime(g.AccessExpiresAt), payload, formatUserTime(g.UpdatedAt), g.ID, m.ExpectedGeneration)
	}
	if err != nil {
		return mcpMutationError(err)
	}
	audit := m.Audit
	audit.Reason = "Worker MCP authorization changed"
	if m.Operation == mcpcmd.GrantRenew {
		audit.Reason = "Worker MCP credentials renewed"
	}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return mcpMutationError(err)
	}
	if m.Authority != nil {
		if err := authority.checkTime(*m.Authority); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return mcpMutationError(err)
	}
	return nil
}

func validateMCPGrantMutation(m MCPGrantMutation) error {
	g := m.Grant
	b := g.Binding
	if g.ID == "" || len(g.ID) > 256 || b.ConnectionID == "" || len(b.ConnectionID) > 256 || b.Resource == "" || len(b.Resource) > 8192 || b.Issuer == "" || len(b.Issuer) > 8192 || b.ClientID == "" || len(b.ClientID) > 1024 || m.ExpectedGeneration >= math.MaxInt64 || g.Generation != m.ExpectedGeneration+1 || g.CreatedAt.IsZero() || g.UpdatedAt.Before(g.CreatedAt) || len(g.Scopes) > 64 {
		return mcpcmd.ErrInvalid
	}
	if len(g.ProtectedValues) > (128<<10)+29 || (len(g.ProtectedValues) > 0 && (len(g.ProtectedValues) < 29 || g.ProtectedValues[0] != 1)) {
		return mcpcmd.ErrInvalid
	}
	if g.Status == mcpcmd.GrantAuthorized && len(g.ProtectedValues) == 0 {
		return mcpcmd.ErrInvalid
	}
	switch g.TokenEndpointAuthMethod {
	case "none", "client_secret_basic", "client_secret_post":
	default:
		return mcpcmd.ErrInvalid
	}
	for _, scope := range g.Scopes {
		if scope == "" || len(scope) > 256 || strings.ContainsAny(scope, "\x00\r\n") {
			return mcpcmd.ErrInvalid
		}
	}
	if err := usercmd.ValidateAuditEvent(m.Audit); err != nil || m.Audit.TargetType != usercmd.AuditTargetMCP || m.Audit.TargetID != b.ConnectionID || m.Audit.Outcome != usercmd.AuditOutcomeSucceeded || m.Audit.OccurredAt.After(g.UpdatedAt) {
		return mcpcmd.ErrInvalid
	}
	if m.Operation == mcpcmd.GrantRenew {
		if m.Authority != nil || m.Audit.ActorUserID != "" || m.Audit.ActorSessionID != "" || m.Audit.Action != usercmd.AuditActionMCPCredentialsRenewed {
			return mcpcmd.ErrInvalid
		}
	} else {
		if m.Authority == nil || validateMCPAuthority(*m.Authority) != nil || m.Audit.Action != usercmd.AuditActionMCPAuthorizationChanged || m.Audit.ActorUserID != m.Authority.UserID || m.Audit.ActorSessionID != m.Authority.SessionID || !m.Authority.At.Equal(m.Audit.OccurredAt) {
			return mcpcmd.ErrInvalid
		}
	}
	switch m.Operation {
	case mcpcmd.GrantRegister:
		if g.Status != mcpcmd.GrantAuthRequired {
			return mcpcmd.ErrInvalid
		}
	case mcpcmd.GrantAuthorize:
		if g.Status != mcpcmd.GrantAuthorized || m.ExpectedRevisionID == "" || len(m.ExpectedRevisionID) > 256 {
			return mcpcmd.ErrInvalid
		}
	case mcpcmd.GrantRenew:
		if g.Status != mcpcmd.GrantAuthorized && g.Status != mcpcmd.GrantAuthRequired {
			return mcpcmd.ErrInvalid
		}
	case mcpcmd.GrantDisconnect:
		if g.Status != mcpcmd.GrantDisconnected {
			return mcpcmd.ErrInvalid
		}
	default:
		return mcpcmd.ErrInvalid
	}
	return nil
}

func (s *sqlMCPStore) grantByID(ctx context.Context, q userQueryer, id string, lock bool) (mcpcmd.Grant, bool, error) {
	query := s.users.bind(`SELECT ` + mcpGrantColumns + ` FROM balda_mcp_grants WHERE grant_id = ?`)
	if lock {
		query += s.users.forUpdate
	}
	return readMCPGrant(q.QueryRowContext(ctx, query, id))
}

func (s *sqlMCPStore) GetMCPGrant(ctx context.Context, b mcpcmd.AuthBinding) (mcpcmd.Grant, bool, error) {
	return readMCPGrant(s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT `+mcpGrantColumns+` FROM balda_mcp_grants WHERE connection_id = ? AND resource = ? AND issuer = ? AND client_id = ?`), b.ConnectionID, b.Resource, b.Issuer, b.ClientID))
}

func readMCPGrant(row interface{ Scan(dest ...any) error }) (mcpcmd.Grant, bool, error) {
	g, err := scanMCPGrant(row)
	if errors.Is(err, sql.ErrNoRows) {
		return mcpcmd.Grant{}, false, nil
	}
	if err != nil {
		return mcpcmd.Grant{}, false, mcpStoreError("read MCP grant", err)
	}
	return g, true, nil
}

func scanMCPGrant(row interface{ Scan(dest ...any) error }) (mcpcmd.Grant, error) {
	var g mcpcmd.Grant
	var scopes, clientExpiry, accessExpiry, created, updated string
	err := row.Scan(&g.ID, &g.Binding.ConnectionID, &g.Binding.Resource, &g.Binding.Issuer, &g.Binding.ClientID, &g.Generation, &g.Status, &scopes, &g.TokenEndpointAuthMethod, &clientExpiry, &accessExpiry, &g.ProtectedValues, &created, &updated)
	if err != nil {
		return mcpcmd.Grant{}, err
	}
	if len(g.ProtectedValues) == 0 {
		g.ProtectedValues = nil
	}
	if err := json.Unmarshal([]byte(scopes), &g.Scopes); err != nil {
		return mcpcmd.Grant{}, err
	}
	for _, stamp := range []struct {
		raw    string
		target *time.Time
	}{{clientExpiry, &g.ClientSecretExpiresAt}, {accessExpiry, &g.AccessExpiresAt}, {created, &g.CreatedAt}, {updated, &g.UpdatedAt}} {
		value, err := parseUserTime(stamp.raw)
		if err != nil {
			return mcpcmd.Grant{}, err
		}
		*stamp.target = value
	}
	return g, nil
}

func (s *sqlMCPStore) ListMCPGrants(ctx context.Context) ([]mcpcmd.Grant, error) {
	rows, err := s.users.db.QueryContext(ctx, `SELECT `+mcpGrantColumns+` FROM balda_mcp_grants ORDER BY connection_id, grant_id`)
	if err != nil {
		return nil, mcpStoreError("list MCP grants", err)
	}
	defer func() { _ = rows.Close() }()
	var grants []mcpcmd.Grant
	for rows.Next() {
		g, err := scanMCPGrant(rows)
		if err != nil {
			return nil, mcpStoreError("decode MCP grant", err)
		}
		grants = append(grants, g)
	}
	if err := rows.Err(); err != nil {
		return nil, mcpStoreError("iterate MCP grants", err)
	}
	return grants, nil
}
