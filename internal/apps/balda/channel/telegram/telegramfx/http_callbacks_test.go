package telegramfx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

func TestGatewayCallbackKeepsTelegramTokenCheck(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer api.Close()
	telegramClient, err := client.NewClientWithResponses(api.URL)
	if err != nil {
		t.Fatal(err)
	}
	source, err := tgbotkit.NewUpdateSource(tgbotkit.Config{Webhook: tgbotkit.WebhookConfig{
		Enabled: true, URL: "https://example.com/telegram/webhook", AuthToken: "secret",
	}}, telegramClient, nil, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	callbacks, err := NewGatewayCallbackProvider(source)(context.Background())
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
	registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/balda/gateway/telegram/webhook", strings.NewReader(`{"update_id":1}`)))
	if recorder.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", recorder.Code)
	}
}
