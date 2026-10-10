package mattermostfx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

type callbackProcessor struct{}

func (callbackProcessor) ProcessInbound(context.Context, mattermost.InboundMessage) (turncmd.InboundSettlement, error) {
	return turncmd.InboundSettlement{}, nil
}
func (callbackProcessor) HandleCommand(context.Context, mattermost.InboundCommand) error { return nil }
func (callbackProcessor) HandleUnsupportedCommand(context.Context, mattermost.InboundCommand) error {
	return nil
}

func TestGatewayCallbackKeepsMattermostTokenCheck(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(mattermost.User{ID: "bot-1", Username: "balda"})
	}))
	defer api.Close()
	server := mattermost.NewCommandServer(mattermost.CommandServerParams{
		Processor: callbackProcessor{}, Commands: commandcmd.NewRegistryWithAdvertisements(nil),
		Client: mattermost.NewClient(api.URL, "bot-token", "bot-1"),
		Config: mattermost.CommandServerConfig{Enabled: true, Token: "secret", BotUserID: "bot-1", BotUsername: "balda"},
		Logger: zerolog.Nop(),
	})
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
	for _, routePath := range []string{"/balda/gateway/mattermost/commands", "/mattermost/commands"} {
		recorder := httptest.NewRecorder()
		registry.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, routePath, strings.NewReader("token=wrong")))
		if recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s: status = %d, want 401", routePath, recorder.Code)
		}
	}
}
