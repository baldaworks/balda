package webhookfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

func TestModuleWiring(t *testing.T) {
	app := fx.New(
		fx.NopLogger,
		fx.Provide(func() webhook.Config { return webhook.Config{} }, zerolog.Nop),
		Module,
	)
	if err := app.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := app.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReceiverWiringRequiresReceiptForAcknowledgedRoute(t *testing.T) {
	config := webhook.Config{Enabled: true, Routes: map[string]webhook.RouteConfig{"event": {
		Path: "/event", PromptTemplate: "{{.RawBody}}",
		Envelope: webhook.RouteEnvelopeConfig{ReportTo: &webhook.RouteTargetConfig{Target: "managed_alias", Key: "main_chat"}, AckOnDelivery: true},
	}}}
	service := webhookapp.NewService(nil, nil, nil)
	_, err := newReceiver(receiverParams{Config: config, Service: service, Logger: zerolog.Nop()})
	if err == nil || !strings.Contains(err.Error(), "delivery receipt store") {
		t.Fatalf("newReceiver() = %v", err)
	}
	config.Enabled = false
	if _, err := newReceiver(receiverParams{Config: config, Service: service, Logger: zerolog.Nop()}); err != nil {
		t.Fatalf("disabled receiver should not need receipts: %v", err)
	}
}
