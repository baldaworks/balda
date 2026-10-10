package zulipfx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/rs/zerolog"
)

func TestGatewayCallbackKeepsZulipTokenCheck(t *testing.T) {
	server := zulip.NewServer(zulip.ServerParams{ZulipEnabled: true, ZulipWebhookToken: "secret", Logger: zerolog.Nop()})
	callbacks, err := NewGatewayCallbackProvider(server)(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddGatewayCallbacks(callbacks); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/balda/gateway/zulip/webhook", strings.NewReader(`{"token":"wrong"}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}
