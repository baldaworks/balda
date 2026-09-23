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
}
