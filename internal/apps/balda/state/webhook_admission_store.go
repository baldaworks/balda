package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
)

type sqlWebhookAdmissionStore struct {
	db       *sql.DB
	bind     func(string) string
	postgres bool
	users    *sqlUserStore
}

var _ WebhookAdmissionStore = (*sqlWebhookAdmissionStore)(nil)

const webhookAdmissionColumns = `route_name, dedupe_key, request_id, prompt, job_id, session_id,
	report_locator_json, created_at, message_id, stream, sequence, raw_body, source`

func (s *sqlWebhookAdmissionStore) Get(ctx context.Context, routeName, dedupeKey string) (webhookcmd.Admission, bool, error) {
	record, err := scanWebhookAdmission(s.db.QueryRowContext(ctx, s.bind(`SELECT `+webhookAdmissionColumns+`
		FROM balda_webhook_admissions WHERE route_name = ? AND dedupe_key = ?`), routeName, dedupeKey))
	if errors.Is(err, sql.ErrNoRows) {
		return webhookcmd.Admission{}, false, nil
	}
	if err != nil {
		return webhookcmd.Admission{}, false, s.wrapError("read webhook admission", err)
	}
	return record, true, nil
}

func (s *sqlWebhookAdmissionStore) GetByJobID(ctx context.Context, jobID string) (webhookcmd.Admission, bool, error) {
	record, err := scanWebhookAdmission(s.db.QueryRowContext(ctx, s.bind(`SELECT `+webhookAdmissionColumns+`
		FROM balda_webhook_admissions WHERE job_id = ?`), strings.TrimSpace(jobID)))
	if errors.Is(err, sql.ErrNoRows) {
		return webhookcmd.Admission{}, false, nil
	}
	if err != nil {
		return webhookcmd.Admission{}, false, s.wrapError("read webhook admission by job", err)
	}
	return record, true, nil
}

func (s *sqlWebhookAdmissionStore) Create(ctx context.Context, candidate webhookcmd.Admission) (webhookcmd.Admission, bool, error) {
	if candidate.Source == "" {
		candidate.Source = WebhookHistorySourceExternal
	}
	if err := validateWebhookAdmission(candidate); err != nil {
		return webhookcmd.Admission{}, false, err
	}
	var reportJSON any
	if candidate.ReportTo != nil {
		encoded, err := json.Marshal(candidate.ReportTo)
		if err != nil {
			return webhookcmd.Admission{}, false, fmt.Errorf("encode webhook report locator: %w", err)
		}
		reportJSON = string(encoded)
	}
	var rawBody any
	if candidate.RawBody != nil {
		rawBody = []byte(*candidate.RawBody)
	}
	query := s.bind(`INSERT INTO balda_webhook_admissions
		(route_name, dedupe_key, request_id, prompt, job_id, session_id, report_locator_json, created_at, raw_body, source)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (route_name, dedupe_key) DO NOTHING`)
	args := []any{
		candidate.RouteName, candidate.DedupeKey, candidate.RequestID, candidate.Prompt,
		candidate.JobID, candidate.SessionID, reportJSON, formatWebhookHistoryTime(candidate.CreatedAt),
		rawBody, candidate.Source,
	}
	var result sql.Result
	var err error
	if candidate.TestAuthority != nil {
		result, err = s.createAuthorizedTest(ctx, candidate, query, args)
	} else {
		result, err = s.db.ExecContext(ctx, query, args...)
	}
	if err != nil {
		if errors.Is(err, ErrWebhookRouteForbidden) || errors.Is(err, ErrWebhookRouteConflict) ||
			errors.Is(err, ErrWebhookRouteNotFound) || errors.Is(err, ErrWebhookRouteInvalid) {
			return webhookcmd.Admission{}, false, err
		}
		return webhookcmd.Admission{}, false, s.wrapError("create webhook admission", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return webhookcmd.Admission{}, false, s.wrapError("check webhook admission insert", err)
	}
	record, found, err := s.Get(ctx, candidate.RouteName, candidate.DedupeKey)
	if err != nil {
		return webhookcmd.Admission{}, false, err
	}
	if !found {
		return webhookcmd.Admission{}, false, fmt.Errorf("webhook admission disappeared after insert")
	}
	return record, count == 1, nil
}

func (s *sqlWebhookAdmissionStore) createAuthorizedTest(ctx context.Context, candidate webhookcmd.Admission,
	query string, args []any) (sql.Result, error) {
	if s.users == nil || candidate.Source != WebhookHistorySourceTest {
		return nil, ErrWebhookRouteInvalid
	}
	a := candidate.TestAuthority
	authority := WebhookRouteAuthority{UserID: a.UserID, SessionID: a.SessionID,
		UserVersion: a.UserVersion, CredentialVersion: a.CredentialVersion,
		MFAVersion: a.MFAVersion, SessionVersion: a.SessionVersion, At: a.At}
	if !validWebhookRouteAuthority(authority) || a.RouteVersion == 0 {
		return nil, ErrWebhookRouteInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return nil, ErrWebhookRouteUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := s.users.checkBrowserAuthority(ctx, tx, webhookBrowserAuthority(authority))
	if err != nil {
		return nil, webhookAuthorityError(err)
	}
	routeQuery := `SELECT enabled, deleted, definition_version FROM balda_webhook_routes WHERE name = ?`
	if s.postgres {
		routeQuery += ` FOR SHARE`
	}
	var enabled, deleted int
	var version uint64
	if err := tx.QueryRowContext(ctx, s.bind(routeQuery), candidate.RouteName).Scan(&enabled, &deleted, &version); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrWebhookRouteNotFound
		}
		return nil, ErrWebhookRouteUnavailable
	}
	if deleted != 0 {
		return nil, ErrWebhookRouteNotFound
	}
	if version != a.RouteVersion || enabled == 0 && !a.ConfirmDisabled {
		return nil, ErrWebhookRouteConflict
	}
	result, err := tx.ExecContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	count, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if count == 1 {
		audit := usercmd.AuditEvent{ID: candidate.JobID, Action: usercmd.AuditActionWebhookTestRequested,
			Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: a.UserID, ActorSessionID: a.SessionID,
			TargetType: usercmd.AuditTargetWebhookRoute, TargetID: candidate.RouteName,
			Source: "backoffice", OccurredAt: candidate.CreatedAt}
		if err := usercmd.ValidateAuditEvent(audit); err != nil {
			return nil, ErrWebhookRouteInvalid
		}
		if err := s.users.insertAudit(ctx, tx, audit); err != nil {
			return nil, ErrWebhookRouteUnavailable
		}
	}
	if err := locked.checkTime(webhookBrowserAuthority(authority)); err != nil {
		return nil, webhookAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return nil, ErrWebhookRouteUnavailable
	}
	return result, nil
}

func (s *sqlWebhookAdmissionStore) RecordReceipt(ctx context.Context, routeName, dedupeKey string, receipt webhookcmd.Receipt) (webhookcmd.Admission, error) {
	if strings.TrimSpace(routeName) == "" || strings.TrimSpace(dedupeKey) == "" ||
		strings.TrimSpace(receipt.MessageID) == "" || len(receipt.MessageID) > webhookcmd.MaxReceiptMessageBytes ||
		strings.TrimSpace(receipt.Stream) == "" || len(receipt.Stream) > webhookcmd.MaxReceiptStreamBytes ||
		receipt.Sequence > math.MaxInt64 {
		return webhookcmd.Admission{}, fmt.Errorf("invalid webhook dispatch receipt")
	}
	_, err := s.db.ExecContext(ctx, s.bind(`UPDATE balda_webhook_admissions
		SET message_id = ?, stream = ?, sequence = ?
		WHERE route_name = ? AND dedupe_key = ? AND message_id = ''`),
		receipt.MessageID, receipt.Stream, receipt.Sequence, routeName, dedupeKey)
	if err != nil {
		return webhookcmd.Admission{}, s.wrapError("record webhook dispatch receipt", err)
	}
	record, found, err := s.Get(ctx, routeName, dedupeKey)
	if err != nil {
		return webhookcmd.Admission{}, err
	}
	if !found {
		return webhookcmd.Admission{}, fmt.Errorf("webhook admission disappeared before receipt")
	}
	return record, nil
}

func (s *sqlWebhookAdmissionStore) wrapError(operation string, err error) error {
	if s.postgres {
		return postgresErrorf("%s: %w", operation, err)
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func validateWebhookAdmission(a webhookcmd.Admission) error {
	if strings.TrimSpace(a.RouteName) == "" || strings.TrimSpace(a.DedupeKey) == "" ||
		strings.TrimSpace(a.RequestID) == "" || strings.TrimSpace(a.Prompt) == "" ||
		len(a.RouteName) > webhookcmd.MaxRouteNameBytes ||
		len(a.DedupeKey) > webhookcmd.MaxDedupeKeyBytes ||
		len(a.RequestID) > webhookcmd.MaxRequestIDBytes ||
		len(a.Prompt) > webhookcmd.MaxPromptBytes || strings.TrimSpace(a.JobID) == "" ||
		!strings.HasPrefix(a.SessionID, "wh-") || a.CreatedAt.IsZero() ||
		(a.RawBody != nil && len(*a.RawBody) > webhookcmd.MaxBodyBytes) ||
		(a.Source != WebhookHistorySourceExternal && a.Source != WebhookHistorySourceTest) {
		return fmt.Errorf("invalid webhook admission")
	}
	if a.ReportTo != nil && (a.ReportTo.ChannelType == "" || a.ReportTo.AddressKey == "" ||
		a.ReportTo.AddressJSON == "" || a.ReportTo.SessionID == "") {
		return fmt.Errorf("invalid webhook report locator")
	}
	return nil
}

func scanWebhookAdmission(row interface{ Scan(dest ...any) error }) (webhookcmd.Admission, error) {
	var record webhookcmd.Admission
	var reportJSON sql.NullString
	var rawBody sql.Null[[]byte]
	var createdAt string
	var sequence int64
	if err := row.Scan(&record.RouteName, &record.DedupeKey, &record.RequestID, &record.Prompt,
		&record.JobID, &record.SessionID, &reportJSON, &createdAt,
		&record.MessageID, &record.Stream, &sequence, &rawBody, &record.Source); err != nil {
		return webhookcmd.Admission{}, err
	}
	if sequence < 0 {
		return webhookcmd.Admission{}, fmt.Errorf("negative webhook receipt sequence")
	}
	record.Sequence = uint64(sequence)
	if rawBody.Valid {
		body := string(rawBody.V)
		record.RawBody = &body
	}
	var err error
	record.CreatedAt, err = parseUserTime(createdAt)
	if err != nil {
		return webhookcmd.Admission{}, err
	}
	if reportJSON.Valid {
		var locator deliverycmd.Locator
		if err := json.Unmarshal([]byte(reportJSON.String), &locator); err != nil {
			return webhookcmd.Admission{}, err
		}
		record.ReportTo = &locator
	}
	return record, nil
}
