package main

import (
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/appconfig"
)

func TestLoadDatabaseDefaults(t *testing.T) {
	dir := t.TempDir()
	doc := loadDatabaseTestDocument(t, dir)
	if doc.Balda.Database.Type != databaseTypeSQLite || doc.Balda.Database.SQLite.Path != state.DefaultSQLitePath {
		t.Fatalf("database defaults = %v", doc.Balda.Database)
	}
	stateDir := filepath.Join(dir, doc.Balda.StateDir)
	resolved, err := doc.Balda.Database.Resolve(dir, stateDir)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.SQLite.Path != filepath.Join(stateDir, "state.db") {
		t.Fatalf("database default changed existing path: %q", resolved.SQLite.Path)
	}
}

func TestLoadDatabaseEnvironmentOverrides(t *testing.T) {
	dir := t.TempDir()
	for key, value := range map[string]string{
		"BALDA_DATABASE_TYPE":              "postgres",
		"BALDA_DATABASE_SQLITE_PATH":       "{{.StateDir}}/custom.db",
		"BALDA_DATABASE_POSTGRES_HOST":     "database.example",
		"BALDA_DATABASE_POSTGRES_PORT":     "6432",
		"BALDA_DATABASE_POSTGRES_NAME":     "runtime",
		"BALDA_DATABASE_POSTGRES_USER":     "runtime-user",
		"BALDA_DATABASE_POSTGRES_PASSWORD": "test-password",
		"BALDA_DATABASE_POSTGRES_SSLMODE":  "verify-full",
	} {
		t.Setenv(key, value)
	}
	doc := loadDatabaseTestDocument(t, dir)
	want := state.DatabaseConfig{
		Type:     "postgres",
		SQLite:   state.SQLiteConfig{Path: "{{.StateDir}}/custom.db"},
		Postgres: state.PostgresConfig{Host: "database.example", Port: 6432, Name: "runtime", User: "runtime-user", Password: "test-password", SSLMode: "verify-full"},
	}
	if doc.Balda.Database != want {
		t.Fatal("database environment overrides were not applied")
	}
}

func TestInitDatabaseUsesEffectiveStateSettings(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BALDA_STATE_DIR", "runtime-state")
	t.Setenv("BALDA_DATABASE_SQLITE_PATH", "{{.StateDir}}/nested/custom.db")
	document := map[string]any{"balda": map[string]any{
		"state_dir": ".config/balda",
		"database":  map[string]any{"type": "sqlite", "sqlite": map[string]any{"path": state.DefaultSQLitePath}},
	}}
	stateDir, database, err := resolveBaldaInitDatabase(dir, document)
	if err != nil {
		t.Fatal(err)
	}
	if stateDir != filepath.Join(dir, "runtime-state") || database.SQLite.Path != filepath.Join(stateDir, "nested", "custom.db") {
		t.Fatalf("resolved init state: %q, %v", stateDir, database)
	}
	t.Setenv("BALDA_DATABASE_TYPE", "postgres")
	document["balda"].(map[string]any)["database"].(map[string]any)["postgres"] = map[string]any{
		"host": "localhost", "port": 5432, "name": "balda", "user": "balda", "password": "", "sslmode": "disable",
	}
	_, database, err = resolveBaldaInitDatabase(dir, document)
	if err != nil || database.Type != "postgres" {
		t.Fatalf("postgres init selection = %v, %v", database, err)
	}
}

func loadDatabaseTestDocument(t *testing.T, dir string) baldaTestConfigDocument {
	t.Helper()
	if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
`); err != nil {
		t.Fatal(err)
	}
	var doc baldaTestConfigDocument
	_, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{WorkingDir: dir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
		&doc,
	)
	if err != nil {
		t.Fatal(err)
	}
	return doc
}
