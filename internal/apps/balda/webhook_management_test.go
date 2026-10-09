package balda

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
)

func TestConfiguredWebhookRoutesKeepDisabledMetadataWithoutSecrets(t *testing.T) {
	routes := configuredWebhookRoutes(webhook.Config{Routes: map[string]webhook.RouteConfig{
		"events": {
			Path: "events", PromptTemplate: "{{ .RawBody }}",
			Envelope: webhook.RouteEnvelopeConfig{ReportTo: &webhook.RouteTargetConfig{
				Target: "alias", Key: "owner",
			}},
			Auth: webhook.RouteAuthConfig{Type: "header", Header: "X-Token", Value: "private"},
		},
	}})
	if len(routes) != 1 {
		t.Fatalf("routes = %d, want 1", len(routes))
	}
	r := routes[0]
	if r.Enabled || r.Path != "/events" || r.ReportToKind != "alias" || r.AuthHeader != "X-Token" {
		t.Fatalf("disabled config metadata = %+v", r)
	}
}
