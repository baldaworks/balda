package balda

import (
	"net"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/appconfig"
)

const sharedTestBasePath = "/balda"

func browserLoopbackAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}

func TestBackofficeProjectionConfigRejectsInvalidSharedHTTPBeforeStartup(t *testing.T) {
	cfg := BaldaConfig{HTTP: HTTPConfig{BaseURL: "https://balda.example.com/extra"}}
	_, err := backofficeProjectionConfig(cfg, appconfig.RuntimeConfig{}, "", state.DatabaseConfig{})
	if err == nil || !strings.Contains(err.Error(), "balda.http.base_url") {
		t.Fatalf("invalid shared origin error = %v", err)
	}
}

func TestBackofficeProjectionConfigUsesSharedDatabaseAndSafeCapabilities(t *testing.T) {
	database := state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: "/tmp/balda-test.db"}}
	cfg := BaldaConfig{}
	cfg.Telegram.Token = "secret-telegram-token"
	cfg.Webhooks.Enabled = true
	cfg.Webhooks.Routes = map[string]WebhookRouteConfig{"example": {PromptTemplate: "secret prompt"}}
	cfg.Mattermost = MattermostConfig{Enabled: true, ServerURL: "https://mattermost.example", Token: "secret-mattermost-token", CommandsEnabled: true}

	resolved, err := backofficeProjectionConfig(cfg, appconfig.RuntimeConfig{}, "", database)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Database.SQLite.Path != database.SQLite.Path {
		t.Fatalf("database path = %q, want %q", resolved.Database.SQLite.Path, database.SQLite.Path)
	}
	if !resolved.Balda.Telegram.Enabled || resolved.Balda.Webhooks.RouteCount != 1 {
		t.Fatalf("capability projection = %+v", resolved.Balda)
	}
	if !resolved.Balda.Mattermost.Enabled || resolved.Balda.Mattermost.ServerURL != cfg.Mattermost.ServerURL {
		t.Fatalf("Mattermost capability projection = %+v", resolved.Balda.Mattermost)
	}
}

func TestBackofficeProjectionConfigProjectsAgent(t *testing.T) {
	runtimeCfg := appconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{
		"primary": {
			Type:     agentconfig.AgentTypeCodexACP,
			CodexACP: &agentconfig.ACPConfig{Model: "gpt-6-sol", ReasoningEffort: "high"},
		},
	}}
	cfg := BaldaConfig{Provider: "primary", GlobalInstruction: "operate safely"}
	resolved, err := backofficeProjectionConfig(cfg, runtimeCfg, "production", state.DatabaseConfig{})
	if err != nil {
		t.Fatal(err)
	}
	want := backoffice.AgentConfig{ProviderID: "primary", ProviderType: "codex_acp", Profile: "production", Model: "gpt-6-sol", ReasoningEffort: "high", GlobalInstruction: "operate safely"}
	if resolved.Agent != want {
		t.Fatalf("agent projection = %+v, want %+v", resolved.Agent, want)
	}
}

func TestBackofficeProjectionConfigDefersManagedAgentProviderResolution(t *testing.T) {
	resolved, err := backofficeProjectionConfig(BaldaConfig{Provider: "managed"}, appconfig.RuntimeConfig{}, "default", state.DatabaseConfig{})
	if err != nil {
		t.Fatalf("backofficeProjectionConfig() error = %v", err)
	}
	if resolved.Agent.ProviderID != "managed" || resolved.Agent.ProviderType != "" {
		t.Fatalf("agent projection = %+v, want unresolved managed provider", resolved.Agent)
	}
}
