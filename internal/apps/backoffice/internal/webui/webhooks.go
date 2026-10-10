package webui

import (
	"net/url"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

const webhookSourceConfiguration = "Configuration"

// WebhooksView is a route inventory or one guarded route detail.
type WebhooksView struct {
	Rows   []WebhookRow
	Editor *WebhookEditor
}

// WebhookRow contains secret-free inventory metadata.
type WebhookRow struct {
	Name, DetailPath, Source, URL, ReportTo, State string
	Enabled, ReadOnly, Deleted                     bool
	Version                                        uint64
}

// WebhookEditor contains the definition shown on one guarded detail page.
type WebhookEditor struct {
	Row                                          WebhookRow
	New                                          bool
	Name, Path, PromptTemplate, ReportTo, Action string
	URLPrefix                                    string
	AckOnDelivery                                bool
	DedupeSource, DedupeHeader                   string
	AuthLabel                                    string
	// Secret is populated only by create and rotate POST responses.
	Secret                                    string
	TestRequestKey, TestBody, NextHistoryPath string
	History                                   []WebhookHistoryRow
	HistoryDetail                             *WebhookHistoryDetail
	HistoryLoaded                             bool
}

// WebhookHistoryRow contains one admitted request's summary.
type WebhookHistoryRow struct {
	Source, Requested, State, DeliveryState, DetailPath string
}

// WebhookHistoryDetail contains safe input, output and delivery information.
type WebhookHistoryDetail struct {
	Input, Output, ReportTo, DeliveryState, DeliveryPayload string
	InputAvailable, HasReportTo                             bool
}

// ProjectWebhookHistoryRow presents a durable admission without private identifiers.
func ProjectWebhookHistoryRow(item webhookroutecmd.HistoryItem, routePath string) WebhookHistoryRow {
	source := "External"
	if item.Source == "test" {
		source = "Test POST"
	}
	delivery := item.DeliveryStatus
	if !item.HasReportTo {
		delivery = "No report"
	} else if delivery == "" {
		delivery = "Pending"
	}
	return WebhookHistoryRow{Source: source, Requested: item.CreatedAt.UTC().Format(time.RFC3339),
		State: item.JobStatus, DeliveryState: delivery,
		DetailPath: routePath + "?job_id=" + url.QueryEscape(item.JobID)}
}

// ProjectWebhookHistoryDetail displays exact valid text or a lossless hex input.
func ProjectWebhookHistoryDetail(item webhookroutecmd.HistoryItem) *WebhookHistoryDetail {
	return &WebhookHistoryDetail{Input: item.Input, InputAvailable: item.InputAvailable,
		Output: item.Output, HasReportTo: item.HasReportTo, ReportTo: item.ReportTo,
		DeliveryState: item.DeliveryStatus, DeliveryPayload: item.DeliveryPayload}
}

// ProjectWebhookRow omits instruction content and all authentication values.
func ProjectWebhookRow(item webhookroutecmd.Item) WebhookRow {
	row := WebhookRow{Name: item.Definition.Name,
		DetailPath: "/webhooks/" + url.PathEscape(item.Definition.Name), ReportTo: item.Definition.ReportTo,
		Enabled: item.Enabled, Deleted: item.Deleted, Version: item.Version,
		ReadOnly: item.Source != webhookroutecmd.SourceManaged, Source: "Backoffice", State: "Enabled"}
	if row.ReadOnly {
		row.Source = webhookSourceConfiguration
	}
	if !row.Enabled {
		row.State = "Disabled"
	}
	if row.Deleted {
		row.State = "Archived"
	}
	if row.ReportTo == "" {
		row.ReportTo = "No report"
	}
	return row
}

// ProjectWebhookEditor exposes the guarded definition without a saved secret.
func ProjectWebhookEditor(item webhookroutecmd.Item, create bool) *WebhookEditor {
	d := item.Definition
	e := &WebhookEditor{Row: ProjectWebhookRow(item), New: create,
		Name: d.Name, Path: d.Path, PromptTemplate: d.PromptTemplate, ReportTo: d.ReportTo,
		AckOnDelivery: d.AckOnDelivery, DedupeSource: d.DedupeSource,
		DedupeHeader: d.DedupeHeader, Action: "/webhooks/" + url.PathEscape(d.Name),
		AuthLabel: "Generated secret in X-Balda-Webhook-Secret"}
	if item.Source == webhookroutecmd.SourceConfig {
		e.AuthLabel = "None"
		if item.AuthType == webhookroutecmd.AuthTypeHeader {
			e.AuthLabel = "Header: " + item.AuthHeader
		}
	}
	if create {
		e.Action = "/webhooks"
		e.Row.ReadOnly = false
		e.DedupeSource = webhookroutecmd.DedupeSourceRequestID
	}
	return e
}
