package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const webhookHistoryTimeFormat = "2006-01-02T15:04:05.000000000Z"

func formatWebhookHistoryTime(value time.Time) string {
	return value.UTC().Format(webhookHistoryTimeFormat)
}

// The admission is the history row. Optional joins describe work that may still
// be queued, and a delivery that may never exist when Report to is empty.
const webhookHistoryColumns = `a.route_name, a.job_id, a.request_id, a.source, a.raw_body,
	a.prompt, a.report_locator_json, a.created_at,
	COALESCE(j.status, ''), COALESCE(j.result, j.result_json, ''),
	CASE WHEN final.status = 'sent' THEN final.status
		WHEN terminal.status = 'sent' THEN terminal.status
		ELSE COALESCE(final.status, terminal.status, '') END,
	CASE WHEN final.status = 'sent' THEN COALESCE(final.payload, final.payload_json, '')
		WHEN terminal.status = 'sent' THEN COALESCE(terminal.payload, terminal.payload_json, '')
		ELSE COALESCE(final.payload, final.payload_json, terminal.payload, terminal.payload_json, '') END`

const webhookHistoryFrom = ` FROM balda_webhook_admissions AS a
	LEFT JOIN execution_jobs AS j ON j.id = a.job_id
	LEFT JOIN execution_delivery_outbox AS final
		ON final.delivery_key = a.job_id || ':delivery:final' AND final.job_id = a.job_id
	LEFT JOIN execution_delivery_outbox AS terminal
		ON terminal.delivery_key = a.job_id || ':delivery:terminal' AND terminal.job_id = a.job_id`

func (s *sqlWebhookAdmissionStore) ListHistory(
	ctx context.Context, routeName string, beforeAt time.Time, beforeJobID string, limit int,
) ([]WebhookHistoryRecord, error) {
	routeName = strings.TrimSpace(routeName)
	if routeName == "" || beforeAt.IsZero() != (beforeJobID == "") {
		return nil, fmt.Errorf("route name and complete history cursor are required")
	}
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	before := ""
	if !beforeAt.IsZero() {
		before = formatWebhookHistoryTime(beforeAt)
	}
	rows, err := s.db.QueryContext(ctx, s.bind(`SELECT `+webhookHistoryColumns+webhookHistoryFrom+`
		WHERE a.route_name = ? AND (? = '' OR a.created_at < ? OR (a.created_at = ? AND a.job_id < ?))
		ORDER BY a.created_at DESC, a.job_id DESC LIMIT ?`),
		routeName, before, before, before, beforeJobID, limit)
	if err != nil {
		return nil, s.wrapError("list webhook history", err)
	}
	defer func() { _ = rows.Close() }()
	var history []WebhookHistoryRecord
	for rows.Next() {
		record, scanErr := scanWebhookHistory(rows)
		if scanErr != nil {
			return nil, s.wrapError("scan webhook history", scanErr)
		}
		history = append(history, record)
	}
	if err := rows.Err(); err != nil {
		return nil, s.wrapError("iterate webhook history", err)
	}
	return history, nil
}

func (s *sqlWebhookAdmissionStore) GetHistory(
	ctx context.Context, routeName, jobID string,
) (WebhookHistoryRecord, bool, error) {
	if strings.TrimSpace(routeName) == "" || strings.TrimSpace(jobID) == "" {
		return WebhookHistoryRecord{}, false, fmt.Errorf("route name and job id are required")
	}
	record, err := scanWebhookHistory(s.db.QueryRowContext(ctx, s.bind(`SELECT `+
		webhookHistoryColumns+webhookHistoryFrom+`
		WHERE a.route_name = ? AND a.job_id = ?`),
		strings.TrimSpace(routeName), strings.TrimSpace(jobID)))
	if errors.Is(err, sql.ErrNoRows) {
		return WebhookHistoryRecord{}, false, nil
	}
	if err != nil {
		return WebhookHistoryRecord{}, false, s.wrapError("read webhook history", err)
	}
	return record, true, nil
}

func scanWebhookHistory(row interface{ Scan(dest ...any) error }) (WebhookHistoryRecord, error) {
	var record WebhookHistoryRecord
	var rawBody, reportJSON sql.NullString
	var createdAt string
	if err := row.Scan(&record.RouteName, &record.JobID, &record.RequestID, &record.Source,
		&rawBody, &record.Prompt, &reportJSON, &createdAt, &record.JobStatus,
		&record.Output, &record.DeliveryStatus, &record.DeliveryPayload); err != nil {
		return WebhookHistoryRecord{}, err
	}
	if rawBody.Valid {
		record.RawBody = &rawBody.String
	}
	var err error
	record.CreatedAt, err = parseUserTime(createdAt)
	if err != nil {
		return WebhookHistoryRecord{}, fmt.Errorf("parse webhook admission time: %w", err)
	}
	if reportJSON.Valid {
		var locator deliverycmd.Locator
		if err := json.Unmarshal([]byte(reportJSON.String), &locator); err != nil {
			return WebhookHistoryRecord{}, fmt.Errorf("decode webhook report locator: %w", err)
		}
		record.ReportTo = &locator
	}
	if record.JobStatus == "" {
		record.JobStatus = "admitted"
	}
	return record, nil
}
