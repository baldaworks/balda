// Package webhookbackofficeapp adapts webhook route policy to Backoffice.
package webhookbackofficeapp

import (
	"context"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

// Operations delegates guarded route management to its application owner.
type Operations struct {
	service *webhookmanagement.Service
	ingress *webhookapp.Ingress
	history state.WebhookAdmissionStore
}

// New binds the webhook route policy to Backoffice's consuming port.
func New(service *webhookmanagement.Service, ingress *webhookapp.Ingress, history state.WebhookAdmissionStore) *Operations {
	return &Operations{service: service, ingress: ingress, history: history}
}

// TestPost admits one synthetic request using the current route policy.
func (o *Operations) TestPost(ctx context.Context, request webhookroutecmd.TestPost) (webhookroutecmd.TestResult, error) {
	item, err := o.service.Get(ctx, request.Name, request.Authority)
	if err != nil {
		return webhookroutecmd.TestResult{}, err
	}
	if item.Deleted {
		return webhookroutecmd.TestResult{}, webhookroutecmd.ErrNotFound
	}
	if item.Version != request.ExpectedVersion {
		return webhookroutecmd.TestResult{}, webhookroutecmd.ErrConflict
	}
	if !item.Enabled && !request.ConfirmDisabled || len(request.Body) > webhookcmd.MaxBodyBytes {
		return webhookroutecmd.TestResult{}, webhookroutecmd.ErrInvalid
	}
	if !validTestKey(request.RequestKey) {
		return webhookroutecmd.TestResult{}, webhookroutecmd.ErrInvalid
	}
	route, err := o.ingress.PrepareTest(ctx, request.Name)
	if err != nil {
		return webhookroutecmd.TestResult{}, testError(err)
	}
	if item.Source == webhookroutecmd.SourceManaged && route.Version != request.ExpectedVersion {
		return webhookroutecmd.TestResult{}, webhookroutecmd.ErrConflict
	}
	result, err := o.ingress.Admit(ctx, route, webhookcmd.Inbound{
		RequestID: request.RequestKey, Path: route.Path, Method: http.MethodPost,
		RawBody: request.Body, Test: true, TestAuthority: &webhookcmd.TestAuthority{
			UserID: request.Authority.UserID, SessionID: request.Authority.SessionID,
			UserVersion: request.Authority.UserVersion, CredentialVersion: request.Authority.CredentialVersion,
			MFAVersion: request.Authority.MFAVersion, SessionVersion: request.Authority.SessionVersion,
			RouteVersion: request.ExpectedVersion, ConfirmDisabled: request.ConfirmDisabled,
			At: request.Authority.At,
		},
	})
	if err != nil {
		return webhookroutecmd.TestResult{}, testError(err)
	}
	return webhookroutecmd.TestResult{JobID: result.JobID, Duplicate: result.Duplicate}, nil
}

func validTestKey(key string) bool {
	if len(key) != 36 || key[8] != '-' || key[13] != '-' || key[18] != '-' || key[23] != '-' {
		return false
	}
	decoded, err := hex.DecodeString(strings.ReplaceAll(key, "-", ""))
	return err == nil && len(decoded) == 16
}

// History lists admitted external and test requests after an authority check.
func (o *Operations) History(ctx context.Context, name string, beforeAt time.Time, beforeJobID string,
	limit int, authority webhookroutecmd.Authority) ([]webhookroutecmd.HistoryItem, error) {
	if _, err := o.service.Get(ctx, name, authority); err != nil {
		return nil, err
	}
	records, err := o.history.ListHistory(ctx, name, beforeAt, beforeJobID, limit)
	if err != nil {
		return nil, webhookroutecmd.ErrUnavailable
	}
	items := make([]webhookroutecmd.HistoryItem, 0, len(records))
	for _, record := range records {
		items = append(items, projectHistory(record))
	}
	return items, nil
}

// HistoryDetail returns one admitted request owned by the named route.
func (o *Operations) HistoryDetail(ctx context.Context, name, jobID string,
	authority webhookroutecmd.Authority) (webhookroutecmd.HistoryItem, error) {
	if _, err := o.service.Get(ctx, name, authority); err != nil {
		return webhookroutecmd.HistoryItem{}, err
	}
	record, found, err := o.history.GetHistory(ctx, name, jobID)
	if err != nil {
		return webhookroutecmd.HistoryItem{}, webhookroutecmd.ErrUnavailable
	}
	if !found {
		return webhookroutecmd.HistoryItem{}, webhookroutecmd.ErrNotFound
	}
	return projectHistory(record), nil
}

func projectHistory(record state.WebhookHistoryRecord) webhookroutecmd.HistoryItem {
	item := webhookroutecmd.HistoryItem{JobID: record.JobID, Source: record.Source,
		CreatedAt: record.CreatedAt, JobStatus: record.JobStatus, Output: record.Output,
		DeliveryStatus: record.DeliveryStatus, DeliveryPayload: record.DeliveryPayload}
	if record.RawBody != nil {
		item.InputAvailable = true
		item.Input = *record.RawBody
		if !utf8.ValidString(item.Input) {
			item.Input = "Hexadecimal bytes: " + hex.EncodeToString([]byte(item.Input))
		}
	}
	if record.ReportTo != nil {
		item.HasReportTo = true
		item.ReportTo = record.ReportTo.ChannelType + ":" + record.ReportTo.AddressKey
	}
	return item
}

func testError(err error) error {
	switch {
	case errors.Is(err, webhookcmd.ErrRouteNotFound):
		return webhookroutecmd.ErrNotFound
	case webhookcmd.IsInvalidRequest(err):
		return webhookroutecmd.ErrInvalid
	case errors.Is(err, state.ErrWebhookRouteForbidden):
		return webhookroutecmd.ErrForbidden
	case errors.Is(err, state.ErrWebhookRouteConflict):
		return webhookroutecmd.ErrConflict
	case errors.Is(err, state.ErrWebhookRouteNotFound):
		return webhookroutecmd.ErrNotFound
	default:
		return webhookroutecmd.ErrUnavailable
	}
}

func (o *Operations) Inventory(ctx context.Context, authority webhookroutecmd.Authority) ([]webhookroutecmd.Item, error) {
	return o.service.Inventory(ctx, authority)
}

func (o *Operations) Get(ctx context.Context, name string, authority webhookroutecmd.Authority) (webhookroutecmd.Item, error) {
	return o.service.Get(ctx, name, authority)
}

func (o *Operations) Create(ctx context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error) {
	return o.service.Create(ctx, request)
}

func (o *Operations) Update(ctx context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error) {
	return o.service.Update(ctx, request)
}

func (o *Operations) SetEnabled(ctx context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error) {
	return o.service.SetEnabled(ctx, request)
}

func (o *Operations) Delete(ctx context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error) {
	return o.service.Delete(ctx, request)
}

func (o *Operations) Rotate(ctx context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error) {
	return o.service.Rotate(ctx, request)
}
