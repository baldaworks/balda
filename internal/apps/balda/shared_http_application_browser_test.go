package balda

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost"
	"github.com/baldaworks/balda/internal/apps/balda/channel/mattermost/mattermostfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	"github.com/baldaworks/balda/internal/apps/balda/channel/slackagent/slackagentfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram/telegramfx"
	"github.com/baldaworks/balda/internal/apps/balda/channel/webhook"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip"
	"github.com/baldaworks/balda/internal/apps/balda/channel/zulip/zulipfx"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/tgbotkit"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookfx"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutefx"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
)

type applicationZulipProcessor struct{ accepted atomic.Int64 }

func (p *applicationZulipProcessor) ProcessInbound(_ context.Context, _ zulip.InboundMessage) (turncmd.InboundSettlement, error) {
	p.accepted.Add(1)
	return turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}, nil
}
func (*applicationZulipProcessor) HandleCommand(context.Context, zulip.InboundCommand) error {
	return nil
}
func (*applicationZulipProcessor) HandleUnsupportedCommand(context.Context, zulip.InboundCommand) error {
	return nil
}

type applicationMattermostProcessor struct{ accepted atomic.Int64 }

func (*applicationMattermostProcessor) ProcessInbound(context.Context, mattermost.InboundMessage) (turncmd.InboundSettlement, error) {
	return turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}, nil
}
func (p *applicationMattermostProcessor) HandleCommand(context.Context, mattermost.InboundCommand) error {
	p.accepted.Add(1)
	return nil
}
func (*applicationMattermostProcessor) HandleUnsupportedCommand(context.Context, mattermost.InboundCommand) error {
	return nil
}

type applicationCommandSupport struct{}

func (applicationCommandSupport) Supports(_, command string) bool { return command == "locator" }

type applicationBrowserIngress struct {
	server     *httpfx.Server
	zulip      *applicationZulipProcessor
	mattermost *applicationMattermostProcessor
	telegram   <-chan client.Update
}

// TestSharedHTTPApplicationBrowser drives authenticated management, durable
// webhook admission, and checked callbacks through one real local socket.
func TestSharedHTTPApplicationBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	root, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(t.TempDir(), "state.db")
	secretPath := filepath.Join(t.TempDir(), "orders-secret")
	address := freeTestAddress(t)
	for restart := range 2 {
		provider, err := state.NewSQLiteProvider(t.Context(), databasePath)
		if err != nil {
			t.Fatal(err)
		}
		if restart == 0 {
			seedRetainedManagedWebhook(t, databasePath)
		}
		basePath := "/balda"
		if restart != 0 {
			basePath = ""
		}
		ingress := startApplicationBrowserIngress(t, provider, address, basePath, restart == 0)
		phase := "initial"
		if restart != 0 {
			phase = "restart"
		}
		command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/shared-http-application.cjs", "http://"+address, phase, secretPath, basePath)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("shared HTTP %s application E2E: %v\n%s", phase, err, output)
		}
		t.Log(string(output))
		if ingress.zulip.accepted.Load() != 1 || ingress.mattermost.accepted.Load() != 1 {
			t.Fatalf("transport admissions: Zulip=%d, Mattermost=%d; want one each",
				ingress.zulip.accepted.Load(), ingress.mattermost.accepted.Load())
		}
		select {
		case <-ingress.telegram:
		case <-time.After(time.Second):
			t.Fatal("valid Telegram callback did not enqueue an update")
		}
		for _, name := range []string{"retained", "configured"} {
			route, found, err := provider.WebhookRoutes().Get(t.Context(), name)
			if err != nil || !found || route.Name != name {
				t.Fatalf("route %q = %+v, found=%t, error=%v", name, route, found, err)
			}
		}
		if err := ingress.server.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		if err := provider.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func startApplicationBrowserIngress(t *testing.T, provider state.Provider, address, basePath string, bootstrap bool) applicationBrowserIngress {
	t.Helper()
	config := BaldaConfig{HTTP: HTTPConfig{ListenAddr: address, BaseURL: "http://" + address, BasePath: &basePath}}
	backofficeConfig, err := backofficeRuntimeConfig(config, state.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := backoffice.NewRuntime(backofficeConfig, provider)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrap {
		if _, err := runtime.BootstrapAdmin(t.Context(), backoffice.BootstrapInput{
			Username: "superuser", Password: []byte("correct horse battery staple"),
		}); err != nil {
			t.Fatal(err)
		}
	}
	registry, err := httpfx.NewRegistry(basePath)
	if err != nil {
		t.Fatal(err)
	}
	manager := webhookmanagement.New(webhookroutefx.NewStore(provider))
	configured := webhookroutecmd.ConfiguredRoute{Name: "configured",
		PromptTemplate: "Configured: {{.RawBody}}", DedupeSource: webhookroutecmd.DedupeSourceRequestID,
		AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: "X-Configured-Secret", Enabled: true}
	if err := manager.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{configured}); err != nil {
		t.Fatal(err)
	}
	service := webhookapp.NewService(nil, provider.WebhookAdmissions(), browserWebhookPublisher(provider))
	ingress, err := webhookapp.NewIngress(basePath, []webhookapp.ConfiguredRoute{{Name: configured.Name,
		PromptTemplate: configured.PromptTemplate,
		AuthType:       configured.AuthType, AuthHeader: configured.AuthHeader,
		AuthValue: "configured-secret", DedupeSource: configured.DedupeSource}},
		browserWebhookRoutes{store: provider.WebhookRoutes()}, service)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.ConfigureWebhooksOperations(webhookbackofficeapp.New(manager, ingress, provider.WebhookAdmissions())); err != nil {
		t.Fatal(err)
	}
	webhookConfig := webhook.Config{Enabled: true, BasePath: basePath, Routes: map[string]webhook.RouteConfig{
		"configured": {PromptTemplate: configured.PromptTemplate,
			Auth: webhook.RouteAuthConfig{Type: webhook.AuthTypeHeader, Header: configured.AuthHeader, Value: "configured-secret"}},
	}}
	receiver, err := webhook.NewReceiver(webhookConfig, ingress, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	slack := slackagent.NewServer(nil, nil, nil, nil, slackagent.Config{
		Enabled: true, SigningSecret: "slack-signing-secret",
	}, zerolog.Nop())
	zulipProcessor := &applicationZulipProcessor{}
	zulipServer := zulip.NewServer(zulip.ServerParams{Processor: zulipProcessor,
		ZulipEnabled: true, ZulipWebhookToken: "zulip-secret", Logger: zerolog.Nop()})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v4/users/me":
			_, _ = w.Write([]byte(`{"id":"bot-1","username":"balda"}`))
		case "/api/v4/channels/channel-1":
			_, _ = w.Write([]byte(`{"id":"channel-1","type":"D"}`))
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
		}
	}))
	t.Cleanup(upstream.Close)
	mattermostProcessor := &applicationMattermostProcessor{}
	mattermostServer := mattermost.NewCommandServer(mattermost.CommandServerParams{
		Processor: mattermostProcessor, Commands: applicationCommandSupport{},
		Client: mattermost.NewClient(upstream.URL, "bot-token", "bot-1"),
		Config: mattermost.CommandServerConfig{Enabled: true, Token: "mattermost-secret",
			BotUserID: "bot-1", BotUsername: "balda"}, Logger: zerolog.Nop(),
	})
	telegramClient, err := client.NewClientWithResponses(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	telegramSource, err := tgbotkit.NewUpdateSource(tgbotkit.Config{Webhook: tgbotkit.WebhookConfig{
		Enabled: true, URL: "https://example.com/telegram/webhook", AuthToken: "telegram-secret",
	}}, telegramClient, nil, zerolog.Nop())
	if err != nil {
		t.Fatal(err)
	}
	params := applicationLifecycleParams{
		Config: config, HTTPConfig: ResolvedHTTPConfig{ListenAddr: address, BaseURL: "http://" + address, BasePath: basePath},
		Backoffice: runtime, HTTPRegistry: registry, StateProvider: provider, TelegramSource: telegramSource,
		GatewayCallbacks: []httpfx.GatewayCallbackProvider{
			slackagentfx.NewGatewayCallbackProvider(slack),
			zulipfx.NewGatewayCallbackProvider(zulipServer),
			mattermostfx.NewGatewayCallbackProvider(mattermostServer),
			telegramfx.NewGatewayCallbackProvider(telegramSource),
		},
		WebhookHTTP: webhookfx.NewHTTPContribution(receiver, ingress),
	}
	server, err := startSharedHTTPIngress(t.Context(), params)
	if err != nil {
		t.Fatal(err)
	}
	if err := telegramSource.Start(t.Context()); err != nil {
		_ = server.Stop(context.Background())
		t.Fatalf("register Telegram after shared listener: %v", err)
	}
	t.Cleanup(func() { _ = telegramSource.Stop(context.Background()) })
	return applicationBrowserIngress{server: server, zulip: zulipProcessor,
		mattermost: mattermostProcessor, telegram: telegramSource.UpdateChan()}
}

func seedRetainedManagedWebhook(t *testing.T, databasePath string) {
	t.Helper()
	db, err := sql.Open("sqlite", databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	secret := sha256.Sum256([]byte("retained-secret"))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	_, err = db.ExecContext(t.Context(), `INSERT INTO balda_webhook_routes
		(name, source, prompt_template, auth_type, auth_header, secret_verifier, enabled, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, "retained", state.WebhookRouteSourceManaged,
		"Retained: {{.RawBody}}", webhookroutecmd.AuthTypeHeader, webhookroutecmd.ManagedSecretHeader,
		hex.EncodeToString(secret[:]), 1, now, now)
	if err != nil {
		t.Fatal(err)
	}
}
