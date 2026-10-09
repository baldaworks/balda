package webhookfx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

func TestModuleWiring(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(func() webhook.Config { return webhook.Config{ListenAddr: "127.0.0.1:0"} }, zerolog.Nop),
		Module,
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDisabledConfigRouteIsAvailableToTestOnly(t *testing.T) {
	config := webhook.Config{Routes: map[string]webhook.RouteConfig{
		"configured":       {Path: "/configured", PromptTemplate: "event: {{.RawBody}}"},
		"placeholder":      {},
		"invalid-template": {Path: "/invalid", PromptTemplate: "{{"},
	}}
	ingress, err := newIngress(ingressParams{Config: config, Service: webhookapp.NewService(nil, nil, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ingress.PrepareExternal(t.Context(), "/configured", nil); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
		t.Fatalf("disabled external route: %v", err)
	}
	prepared, err := ingress.PrepareTest(t.Context(), "configured")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Path != "/configured" || prepared.Name != "configured" {
		t.Fatalf("test route = %+v", prepared)
	}
	for _, name := range []string{"placeholder", "invalid-template"} {
		if _, err := ingress.PrepareTest(t.Context(), name); !errors.Is(err, webhookcmd.ErrRouteNotFound) {
			t.Errorf("invalid disabled route %q: %v", name, err)
		}
	}
}

func TestReceiverWiringRequiresReceiptForAcknowledgedRoute(t *testing.T) {
	config := webhook.Config{Enabled: true, Routes: map[string]webhook.RouteConfig{"event": {
		Path: "/event", PromptTemplate: "{{.RawBody}}",
		Envelope: webhook.RouteEnvelopeConfig{ReportTo: &webhook.RouteTargetConfig{Target: "managed_alias", Key: "main_chat"}, AckOnDelivery: true},
	}}}
	service := webhookapp.NewService(nil, nil, nil)
	ingress, err := newIngress(ingressParams{Config: config, Service: service})
	if err != nil {
		t.Fatal(err)
	}
	_, err = newReceiver(receiverParams{Config: config, Ingress: ingress, Logger: zerolog.Nop()})
	if err == nil || !strings.Contains(err.Error(), "delivery receipt store") {
		t.Fatalf("newReceiver() = %v", err)
	}
	config.Enabled = false
	ingress, err = newIngress(ingressParams{Config: config, Service: service})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newReceiver(receiverParams{Config: config, Ingress: ingress, Logger: zerolog.Nop()}); err != nil {
		t.Fatalf("disabled receiver should not need receipts: %v", err)
	}
}
