package balda

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func TestBackofficeRuntimeConfigUsesSharedDatabaseAndSafeCapabilities(t *testing.T) {
	database := state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: "/tmp/balda-test.db"}}
	cfg := BaldaConfig{}
	cfg.Telegram.Token = "secret-telegram-token"
	cfg.Webhooks.Enabled = true
	cfg.Webhooks.Routes = map[string]WebhookRouteConfig{"example": {PromptTemplate: "secret prompt"}}
	cfg.Mattermost = MattermostConfig{Enabled: true, ServerURL: "https://mattermost.example", Token: "secret-mattermost-token", CommandsEnabled: true, CommandsListenAddr: "127.0.0.1:8094", CommandsPath: "/mattermost/commands", CommandsToken: "secret-command-token"}
	resolved, err := backofficeRuntimeConfig(cfg, database)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Database.SQLite.Path != database.SQLite.Path {
		t.Fatalf("database path = %q, want %q", resolved.Database.SQLite.Path, database.SQLite.Path)
	}
	if !resolved.Balda.Telegram.Enabled || resolved.Balda.Webhooks.RouteCount != 1 {
		t.Fatalf("capability projection = %+v", resolved.Balda)
	}
	if !resolved.Balda.Mattermost.Enabled || !resolved.Balda.Mattermost.CommandsEnabled || resolved.Balda.Mattermost.ListenAddr != cfg.Mattermost.CommandsListenAddr || resolved.Balda.Mattermost.ServerURL != cfg.Mattermost.ServerURL {
		t.Fatalf("Mattermost capability projection = %+v", resolved.Balda.Mattermost)
	}
}
