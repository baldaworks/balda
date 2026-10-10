package balda

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

// configuredWebhookRoutes retains raw declarations for Backoffice inventory
// even when the configured ingress switch disables their HTTP paths.
func configuredWebhookRoutes(cfg webhook.Config) []webhookroutecmd.ConfiguredRoute {
	routes := make([]webhookroutecmd.ConfiguredRoute, 0, len(cfg.Routes))
	for name, raw := range cfg.Routes {
		var reportToKind, reportToKey string
		if raw.Envelope.ReportTo != nil {
			reportToKind = raw.Envelope.ReportTo.Target
			reportToKey = raw.Envelope.ReportTo.Key
		}
		routes = append(routes, webhookroutecmd.ConfiguredRoute{
			Name: name, PromptTemplate: raw.PromptTemplate,
			ReportToKind: reportToKind, ReportToKey: reportToKey,
			AckOnDelivery: raw.Envelope.AckOnDelivery,
			DedupeSource:  raw.Dedupe.Source, DedupeHeader: raw.Dedupe.Header,
			AuthType:   strings.ToLower(strings.TrimSpace(raw.Auth.Type)),
			AuthHeader: raw.Auth.Header, Enabled: cfg.Enabled,
		})
	}
	return routes
}
