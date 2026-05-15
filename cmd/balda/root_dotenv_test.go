package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInitDotEnv_LoadsFromCurrentWorkingDirectory(t *testing.T) {
	workingDir := setWorkingDir(t)
	unsetEnvForTest(t, "BALDA_TELEGRAM_TOKEN")

	if err := writeFile(filepath.Join(workingDir, ".env"), "BALDA_TELEGRAM_TOKEN=from-cwd-dotenv\n"); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	initDotEnv()

	if got := os.Getenv("BALDA_TELEGRAM_TOKEN"); got != "from-cwd-dotenv" {
		t.Fatalf("BALDA_TELEGRAM_TOKEN = %q, want %q", got, "from-cwd-dotenv")
	}
}

func TestInitDotEnv_DoesNotLoadFromDotConfigBalda(t *testing.T) {
	workingDir := setWorkingDir(t)
	unsetEnvForTest(t, "BALDA_TELEGRAM_TOKEN")

	if err := writeFile(filepath.Join(workingDir, ".config", "balda", ".env"), "BALDA_TELEGRAM_TOKEN=from-config-balda-dotenv\n"); err != nil {
		t.Fatalf("write .config/balda/.env: %v", err)
	}

	initDotEnv()

	if got := os.Getenv("BALDA_TELEGRAM_TOKEN"); got != "" {
		t.Fatalf("BALDA_TELEGRAM_TOKEN = %q, want empty (CWD .env only)", got)
	}
}

func unsetEnvForTest(t *testing.T, key string) {
	t.Helper()
	prevValue, wasSet := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatalf("unset %s: %v", key, err)
	}
	t.Cleanup(func() {
		if !wasSet {
			_ = os.Unsetenv(key)
			return
		}
		_ = os.Setenv(key, prevValue)
	})
}
