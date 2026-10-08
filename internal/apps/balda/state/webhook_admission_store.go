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
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
)

type sqlWebhookAdmissionStore struct {
	db       *sql.DB
	bind     func(string) string
	postgres bool
}

var _ WebhookAdmissionStore = (*sqlWebhookAdmissionStore)(nil)

const webhookAdmissionColumns = `route_name, dedupe_key, request_id, prompt, job_id, session_id,
	report_locator_json, created_at, message_id, stream, sequence`

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
	result, err := s.db.ExecContext(ctx, s.bind(`INSERT INTO balda_webhook_admissions
		(route_name, dedupe_key, request_id, prompt, job_id, session_id, report_locator_json, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (route_name, dedupe_key) DO NOTHING`),
		candidate.RouteName, candidate.DedupeKey, candidate.RequestID, candidate.Prompt,
		candidate.JobID, candidate.SessionID, reportJSON, formatUserTime(candidate.CreatedAt))
	if err != nil {
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
		!strings.HasPrefix(a.SessionID, "wh-") || a.CreatedAt.IsZero() {
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
	var createdAt string
	var sequence int64
	if err := row.Scan(&record.RouteName, &record.DedupeKey, &record.RequestID, &record.Prompt,
		&record.JobID, &record.SessionID, &reportJSON, &createdAt,
		&record.MessageID, &record.Stream, &sequence); err != nil {
		return webhookcmd.Admission{}, err
	}
	if sequence < 0 {
		return webhookcmd.Admission{}, fmt.Errorf("negative webhook receipt sequence")
	}
	record.Sequence = uint64(sequence)
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
