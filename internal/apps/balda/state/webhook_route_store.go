package state

import (
	"context"
	"database/sql"
	"encoding/hex"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/jackc/pgx/v5/pgconn"
	"modernc.org/sqlite"
)

type sqlWebhookRouteStore struct{ users *sqlUserStore }

var _ WebhookRouteStore = (*sqlWebhookRouteStore)(nil)

const webhookRouteColumns = `name, source, path, prompt_template, report_to_kind, report_to_key,
	ack_on_delivery, dedupe_source, dedupe_header, auth_type, auth_header,
	enabled, deleted, definition_version, created_at, updated_at`

func (s *sqlWebhookRouteStore) Get(ctx context.Context, name string) (WebhookRouteRecord, bool, error) {
	r, err := scanWebhookRoute(s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT `+webhookRouteColumns+`
		FROM balda_webhook_routes WHERE name = ?`), name))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookRouteRecord{}, false, nil
	}
	if err != nil {
		return WebhookRouteRecord{}, false, ErrWebhookRouteUnavailable
	}
	return r, true, nil
}

func (s *sqlWebhookRouteStore) List(ctx context.Context) ([]WebhookRouteRecord, error) {
	rows, err := s.users.db.QueryContext(ctx, `SELECT `+webhookRouteColumns+`
		FROM balda_webhook_routes WHERE deleted = 0 ORDER BY name`)
	if err != nil {
		return nil, ErrWebhookRouteUnavailable
	}
	defer func() { _ = rows.Close() }()
	var routes []WebhookRouteRecord
	for rows.Next() {
		r, err := scanWebhookRoute(rows)
		if err != nil {
			return nil, ErrWebhookRouteUnavailable
		}
		routes = append(routes, r)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrWebhookRouteUnavailable
	}
	return routes, nil
}

// LookupByPath is for ingress only. It returns a managed secret verifier, never plaintext.
func (s *sqlWebhookRouteStore) LookupByPath(ctx context.Context, path string) (WebhookRouteRecord, bool, error) {
	var r WebhookRouteRecord
	var ack, enabled, deleted int
	var created, updated string
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT `+webhookRouteColumns+`, secret_verifier
		FROM balda_webhook_routes WHERE path = ? AND enabled = 1 AND deleted = 0`), path).
		Scan(&r.Name, &r.Source, &r.Path, &r.PromptTemplate, &r.ReportToKind, &r.ReportToKey,
			&ack, &r.DedupeSource, &r.DedupeHeader, &r.AuthType, &r.AuthHeader,
			&enabled, &deleted, &r.Version, &created, &updated, &r.SecretVerifier)
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookRouteRecord{}, false, nil
	}
	if err != nil {
		return WebhookRouteRecord{}, false, ErrWebhookRouteUnavailable
	}
	if err := finishWebhookRouteScan(&r, ack, enabled, deleted, created, updated); err != nil {
		return WebhookRouteRecord{}, false, ErrWebhookRouteUnavailable
	}
	return r, true, nil
}

func scanWebhookRoute(row interface{ Scan(dest ...any) error }) (WebhookRouteRecord, error) {
	var r WebhookRouteRecord
	var ack, enabled, deleted int
	var created, updated string
	err := row.Scan(&r.Name, &r.Source, &r.Path, &r.PromptTemplate, &r.ReportToKind, &r.ReportToKey,
		&ack, &r.DedupeSource, &r.DedupeHeader, &r.AuthType, &r.AuthHeader,
		&enabled, &deleted, &r.Version, &created, &updated)
	if err != nil {
		return WebhookRouteRecord{}, err
	}
	if err := finishWebhookRouteScan(&r, ack, enabled, deleted, created, updated); err != nil {
		return WebhookRouteRecord{}, err
	}
	return r, nil
}

func finishWebhookRouteScan(r *WebhookRouteRecord, ack, enabled, deleted int, created, updated string) error {
	r.AckOnDelivery, r.Enabled, r.Deleted = ack == 1, enabled == 1, deleted == 1
	var err error
	r.CreatedAt, err = parseUserTime(created)
	if err != nil {
		return err
	}
	r.UpdatedAt, err = parseUserTime(updated)
	return err
}

// ReconcileConfig replaces only config-owned metadata in one transaction.
// Config authentication values are deliberately absent from its records.
func (s *sqlWebhookRouteStore) ReconcileConfig(ctx context.Context, routes []WebhookRouteRecord) error {
	seenNames := make(map[string]bool, len(routes))
	for _, r := range routes {
		if !validWebhookRoute(r) || r.Source != WebhookRouteSourceConfig || r.SecretVerifier != "" || seenNames[r.Name] {
			return ErrWebhookRouteInvalid
		}
		seenNames[r.Name] = true
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return ErrWebhookRouteUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	now := formatUserTime(time.Now().UTC())
	// Disable first so an existing config path can be renamed without a
	// transient uniqueness collision. The transaction hides the intermediate state.
	if _, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_webhook_routes
		SET enabled = 0, deleted = 1, updated_at = ?
		WHERE source = 'config' AND deleted = 0`), now); err != nil {
		return ErrWebhookRouteUnavailable
	}
	for _, r := range routes {
		result, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_webhook_routes
			(name, source, path, prompt_template, report_to_kind, report_to_key, ack_on_delivery,
			dedupe_source, dedupe_header, auth_type, auth_header, secret_verifier,
			enabled, deleted, definition_version, created_at, updated_at)
			VALUES (?, 'config', ?, ?, ?, ?, ?, ?, ?, ?, ?, '', ?, 0, 1, ?, ?)
			ON CONFLICT(name) DO UPDATE SET
				path = excluded.path, prompt_template = excluded.prompt_template,
				report_to_kind = excluded.report_to_kind, report_to_key = excluded.report_to_key,
				ack_on_delivery = excluded.ack_on_delivery, dedupe_source = excluded.dedupe_source,
				dedupe_header = excluded.dedupe_header, auth_type = excluded.auth_type,
				auth_header = excluded.auth_header, secret_verifier = '',
				enabled = excluded.enabled, deleted = 0,
				definition_version = balda_webhook_routes.definition_version + 1,
				updated_at = excluded.updated_at
			WHERE balda_webhook_routes.source = 'config'`),
			r.Name, r.Path, r.PromptTemplate, r.ReportToKind, r.ReportToKey,
			boolInt(r.AckOnDelivery), r.DedupeSource, r.DedupeHeader, r.AuthType,
			r.AuthHeader, boolInt(r.Enabled), now, now)
		if err != nil {
			return webhookRouteWriteError(err)
		}
		count, err := result.RowsAffected()
		if err != nil {
			return ErrWebhookRouteUnavailable
		}
		if count != 1 {
			return ErrWebhookRouteConflict
		}
	}
	if err := tx.Commit(); err != nil {
		return webhookRouteWriteError(err)
	}
	return nil
}

func (s *sqlWebhookRouteStore) CheckAuthority(ctx context.Context, a WebhookRouteAuthority) error {
	if !validWebhookRouteAuthority(a) {
		return ErrWebhookRouteInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return ErrWebhookRouteUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := s.checkAuthority(ctx, tx, a); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return ErrWebhookRouteUnavailable
	}
	return nil
}

func (s *sqlWebhookRouteStore) Save(ctx context.Context, m WebhookRouteMutation) error {
	if !validWebhookRouteMutation(m) {
		return ErrWebhookRouteInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return ErrWebhookRouteUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := s.checkAuthority(ctx, tx, m.Authority)
	if err != nil {
		return err
	}
	if err := locked.checkTime(webhookBrowserAuthority(m.Authority)); err != nil {
		return webhookAuthorityError(err)
	}
	now := formatUserTime(time.Now().UTC())
	r := m.Record
	var result sql.Result
	switch m.Kind {
	case WebhookRouteCreate:
		result, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_webhook_routes
			(name, source, path, prompt_template, report_to_kind, report_to_key, ack_on_delivery,
			dedupe_source, dedupe_header, auth_type, auth_header, secret_verifier,
			enabled, deleted, definition_version, created_at, updated_at)
			VALUES (?, 'managed', ?, ?, ?, ?, ?, ?, ?, 'header', ?, ?, 1, 0, 1, ?, ?)
			ON CONFLICT(name) DO NOTHING`), r.Name, r.Path, r.PromptTemplate,
			r.ReportToKind, r.ReportToKey, boolInt(r.AckOnDelivery), r.DedupeSource,
			r.DedupeHeader, r.AuthHeader, r.SecretVerifier, now, now)
	case WebhookRouteEdit:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_webhook_routes SET
			path = ?, prompt_template = ?, report_to_kind = ?, report_to_key = ?,
			ack_on_delivery = ?, dedupe_source = ?, dedupe_header = ?,
			definition_version = definition_version + 1, updated_at = ?
			WHERE name = ? AND source = 'managed' AND deleted = 0 AND definition_version = ?`),
			r.Path, r.PromptTemplate, r.ReportToKind, r.ReportToKey, boolInt(r.AckOnDelivery),
			r.DedupeSource, r.DedupeHeader, now, r.Name, m.ExpectedVersion)
	case WebhookRouteSelection:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_webhook_routes SET
			enabled = ?, definition_version = definition_version + 1, updated_at = ?
			WHERE name = ? AND source = 'managed' AND deleted = 0 AND definition_version = ?`),
			boolInt(r.Enabled), now, r.Name, m.ExpectedVersion)
	case WebhookRouteDelete:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_webhook_routes SET
			enabled = 0, deleted = 1, definition_version = definition_version + 1, updated_at = ?
			WHERE name = ? AND source = 'managed' AND deleted = 0 AND definition_version = ?`),
			now, r.Name, m.ExpectedVersion)
	case WebhookRouteRotate:
		result, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_webhook_routes SET
			secret_verifier = ?, definition_version = definition_version + 1, updated_at = ?
			WHERE name = ? AND source = 'managed' AND deleted = 0 AND definition_version = ?`),
			r.SecretVerifier, now, r.Name, m.ExpectedVersion)
	}
	if err != nil {
		return webhookRouteWriteError(err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return ErrWebhookRouteUnavailable
	}
	if count != 1 {
		return s.missingMutationError(ctx, tx, m)
	}
	audit := m.Audit
	audit.Reason = webhookMutationReason(m.Kind)
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return ErrWebhookRouteUnavailable
	}
	if err := locked.checkTime(webhookBrowserAuthority(m.Authority)); err != nil {
		return webhookAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return webhookRouteWriteError(err)
	}
	return nil
}

func (s *sqlWebhookRouteStore) missingMutationError(ctx context.Context, tx *sql.Tx, m WebhookRouteMutation) error {
	var source string
	err := tx.QueryRowContext(ctx, s.users.bind(`SELECT source FROM balda_webhook_routes WHERE name = ?`), m.Record.Name).Scan(&source)
	if errors.Is(err, sql.ErrNoRows) {
		if m.Kind == WebhookRouteCreate {
			return ErrWebhookRouteConflict
		}
		return ErrWebhookRouteNotFound
	}
	if err != nil {
		return ErrWebhookRouteUnavailable
	}
	if source != WebhookRouteSourceManaged && m.Kind != WebhookRouteCreate {
		return ErrWebhookRouteForbidden
	}
	return ErrWebhookRouteConflict
}

func (s *sqlWebhookRouteStore) checkAuthority(ctx context.Context, tx *sql.Tx, a WebhookRouteAuthority) (lockedBrowserAuthority, error) {
	locked, err := s.users.checkBrowserAuthority(ctx, tx, webhookBrowserAuthority(a))
	if err != nil {
		return lockedBrowserAuthority{}, webhookAuthorityError(err)
	}
	return locked, nil
}

func webhookBrowserAuthority(a WebhookRouteAuthority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID,
		userVersion: a.UserVersion, credentialVersion: a.CredentialVersion,
		mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func webhookAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return ErrWebhookRouteConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return ErrWebhookRouteForbidden
	default:
		return ErrWebhookRouteUnavailable
	}
}

func validWebhookRouteAuthority(a WebhookRouteAuthority) bool {
	return a.UserID != "" && a.SessionID != "" && a.UserVersion > 0 &&
		a.CredentialVersion > 0 && a.SessionVersion > 0 && !a.At.IsZero()
}

func validWebhookRoute(r WebhookRouteRecord) bool {
	if strings.TrimSpace(r.Name) == "" || strings.TrimSpace(r.Path) == "" ||
		!strings.HasPrefix(r.Path, "/") || strings.TrimSpace(r.PromptTemplate) == "" ||
		(r.ReportToKind == "") != (r.ReportToKey == "") ||
		(r.DedupeSource == "header" && r.DedupeHeader == "") || r.AckOnDelivery && r.ReportToKey == "" {
		return false
	}
	return true
}

func validWebhookRouteMutation(m WebhookRouteMutation) bool {
	r, a := m.Record, m.Authority
	if !validWebhookRouteAuthority(a) || r.Name == "" || r.Source != WebhookRouteSourceManaged ||
		m.ExpectedVersion >= math.MaxInt64 || r.Version != m.ExpectedVersion+1 ||
		(m.Kind == WebhookRouteCreate && (m.ExpectedVersion != 0 || !r.Enabled || r.Deleted)) ||
		(m.Kind != WebhookRouteCreate && m.ExpectedVersion == 0) ||
		(m.Kind == WebhookRouteCreate || m.Kind == WebhookRouteEdit) && !validWebhookRoute(r) ||
		(m.Kind == WebhookRouteCreate && r.AuthHeader == "") ||
		(m.Kind == WebhookRouteCreate || m.Kind == WebhookRouteRotate) && !validWebhookVerifier(r.SecretVerifier) ||
		(m.Kind == WebhookRouteDelete && !r.Deleted) {
		return false
	}
	switch m.Kind {
	case WebhookRouteCreate, WebhookRouteEdit, WebhookRouteSelection, WebhookRouteDelete, WebhookRouteRotate:
	default:
		return false
	}
	return usercmd.ValidateAuditEvent(m.Audit) == nil &&
		m.Audit.Action == usercmd.AuditActionWebhookRouteChanged &&
		m.Audit.TargetType == usercmd.AuditTargetWebhookRoute && m.Audit.TargetID == r.Name &&
		m.Audit.ActorUserID == a.UserID && m.Audit.ActorSessionID == a.SessionID &&
		m.Audit.Outcome == usercmd.AuditOutcomeSucceeded && m.Audit.OccurredAt.Equal(a.At)
}

func validWebhookVerifier(verifier string) bool {
	if len(verifier) != 64 {
		return false
	}
	_, err := hex.DecodeString(verifier)
	return err == nil
}

func webhookMutationReason(kind WebhookRouteMutationKind) string {
	switch kind {
	case WebhookRouteCreate:
		return "Webhook route created"
	case WebhookRouteEdit:
		return "Webhook route edited"
	case WebhookRouteSelection:
		return "Webhook route selection changed"
	case WebhookRouteDelete:
		return "Webhook route deleted"
	case WebhookRouteRotate:
		return "Webhook route secret rotated"
	default:
		return "Webhook route changed"
	}
}

func webhookRouteWriteError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return ErrWebhookRouteConflict
	}
	var sqliteErr *sqlite.Error
	if errors.As(err, &sqliteErr) && sqliteErr.Code()&0xff == 19 {
		return ErrWebhookRouteConflict
	}
	return ErrWebhookRouteUnavailable
}
