package backoffice

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadConfigUsesBaldaFileAndEnvironmentOverrides(t *testing.T) {
	workingDir := t.TempDir()
	configDir := filepath.Join(workingDir, ".config", "balda")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	document := `runtime:
  providers: {}
  mcp_servers: {}
balda:
  working_dir: ""
  state_dir: "runtime-state"
  database:
    type: sqlite
    sqlite:
      path: "{{.StateDir}}/custom.db"
  backoffice:
    listen_addr: "127.0.0.1:9095"
    public_url: "http://127.0.0.1:9095"
    access_token_ttl: "20m"
    refresh_token_ttl: "10h"
`
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(document), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALDA_BACKOFFICE_ACCESS_TOKEN_TTL", "10m")
	config, err := LoadConfig(LoadOptions{WorkingDir: workingDir})
	if err != nil {
		t.Fatal(err)
	}
	if config.Server.ListenAddr != "127.0.0.1:9095" || config.Server.AccessTokenTTL != 10*time.Minute {
		t.Fatalf("server config = %+v", config.Server)
	}
	wantDatabase := filepath.Join(workingDir, "runtime-state", "custom.db")
	if config.Database.SQLite.Path != wantDatabase {
		t.Fatalf("database path = %q, want %q", config.Database.SQLite.Path, wantDatabase)
	}
}
