package state

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveDatabaseConfig(t *testing.T) {
	t.Parallel()
	workingDir := t.TempDir()
	stateDir := filepath.Join(workingDir, "{{.notRecursive}}", "state")
	for _, tt := range []struct {
		name string
		path string
		want string
	}{
		{"default", "", filepath.Join(stateDir, "state.db")},
		{"explicit default", DefaultSQLitePath, filepath.Join(stateDir, "state.db")},
		{"custom", "{{.StateDir}}/custom.db", filepath.Join(stateDir, "custom.db")},
		{"relative", "data/../custom.db", filepath.Join(workingDir, "custom.db")},
		{"absolute", filepath.Join(workingDir, "custom.db"), filepath.Join(workingDir, "custom.db")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := (DatabaseConfig{SQLite: SQLiteConfig{Path: tt.path}}).Resolve(workingDir, stateDir)
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Type != "sqlite" || cfg.SQLite.Path != tt.want {
				t.Fatalf("Resolve() = %v, want sqlite at %q", cfg, tt.want)
			}
		})
	}
}

func TestResolveDatabasePathRejectsInvalidTemplates(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, raw := range []string{
		" ", "{{", "{{.Unknown}}", "{{.StateDir.Unknown}}", "{{printf \"%s\" .StateDir}}",
		"{{.StateDir | printf \"%s\"}}", "{{if .StateDir}}x{{end}}", "{{$x := .StateDir}}{{$x}}",
		"{{define \"other\"}}x{{end}}", "{{template \"database-path\"}}", "a\x00b",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := (DatabaseConfig{SQLite: SQLiteConfig{Path: raw}}).Resolve(dir, dir)
			if err == nil {
				t.Fatal("Resolve() succeeded for invalid path template")
			}
		})
	}
}

func TestResolveSelectedBackendOnly(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	postgres := PostgresConfig{Host: "localhost", Port: 5432, Name: "balda", User: "balda", SSLMode: "require"}
	cfg := DatabaseConfig{Type: "postgres", Postgres: postgres, SQLite: SQLiteConfig{Path: "{{invalid"}}
	if _, err := cfg.Resolve(dir, dir); err != nil {
		t.Fatalf("unselected SQLite rejected: %v", err)
	}
	cfg = DatabaseConfig{Type: "sqlite", Postgres: PostgresConfig{Port: -1}}
	if _, err := cfg.Resolve(dir, dir); err != nil {
		t.Fatalf("unselected PostgreSQL rejected: %v", err)
	}
	for _, kind := range []string{"unknown", "SQLite", " "} {
		if _, err := (DatabaseConfig{Type: kind}).Resolve(dir, dir); err == nil {
			t.Errorf("Resolve(%q) succeeded", kind)
		}
	}
	for _, mutate := range []func(*PostgresConfig){
		func(c *PostgresConfig) { c.Host = "" },
		func(c *PostgresConfig) { c.Name = "" },
		func(c *PostgresConfig) { c.User = "" },
		func(c *PostgresConfig) { c.Port = 0 },
		func(c *PostgresConfig) { c.Port = 65536 },
		func(c *PostgresConfig) { c.SSLMode = "invalid" },
	} {
		invalid := postgres
		mutate(&invalid)
		if _, err := (DatabaseConfig{Type: "postgres", Postgres: invalid}).Resolve(dir, dir); err == nil {
			t.Error("Resolve() succeeded for invalid PostgreSQL configuration")
		}
	}
}

func TestDatabaseConfigRedactsSecrets(t *testing.T) {
	t.Parallel()
	const secret = "database-password-canary"
	cfg := DatabaseConfig{Type: "postgres", Postgres: PostgresConfig{Password: secret}}
	for _, format := range []string{"%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, cfg), secret) {
			t.Errorf("format %q exposed the password", format)
		}
	}
	_, err := cfg.Resolve(t.TempDir(), t.TempDir())
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("expected redacted error, got %v", err)
	}
}
