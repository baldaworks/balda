package webui

import (
	"net/url"

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
	Name, DetailPath, Source, Path, ReportTo, State string
	Enabled, ReadOnly, Deleted                      bool
	Version                                         uint64
}

// WebhookEditor contains the definition shown on one guarded detail page.
type WebhookEditor struct {
	Row                                          WebhookRow
	New                                          bool
	Name, Path, PromptTemplate, ReportTo, Action string
	AckOnDelivery                                bool
	DedupeSource, DedupeHeader                   string
	AuthLabel                                    string
	// Secret is populated only by create and rotate POST responses.
	Secret string
}

// ProjectWebhookRow omits instruction content and all authentication values.
func ProjectWebhookRow(item webhookroutecmd.Item) WebhookRow {
	row := WebhookRow{Name: item.Definition.Name, Path: item.Definition.Path,
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
