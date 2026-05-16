package paths

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigPath(t *testing.T) {
	if got := ConfigPath("/repo"); got != "/repo/.config/balda/config.yaml" {
		t.Fatalf("ConfigPath(/repo) = %q", got)
	}
}

func TestStateDBPath(t *testing.T) {
	if got := StateDBPath("/repo/.config/balda"); got != "/repo/.config/balda/state.db" {
		t.Fatalf("StateDBPath() = %q", got)
	}
}

func TestRequireStateDBReady_AllowsFreshStateDir(t *testing.T) {
	if err := RequireStateDBReady(t.TempDir()); err != nil {
		t.Fatalf("RequireStateDBReady() error = %v", err)
	}
}

func TestRequireStateDBReady_AllowsStateDBWithLegacyFiles(t *testing.T) {
	stateDir := t.TempDir()
	writeTestFile(t, StateDBPath(stateDir))
	writeTestFile(t, filepath.Join(stateDir, "balda.db"))

	if err := RequireStateDBReady(stateDir); err != nil {
		t.Fatalf("RequireStateDBReady() error = %v", err)
	}
}

func TestRequireStateDBReady_RejectsLegacyBaldaDBOnly(t *testing.T) {
	stateDir := t.TempDir()
	legacyPath := filepath.Join(stateDir, "balda.db")
	writeTestFile(t, legacyPath)

	err := RequireStateDBReady(stateDir)
	if err == nil {
		t.Fatal("RequireStateDBReady() error = nil, want legacy database error")
	}
	if !strings.Contains(err.Error(), "legacy state database") ||
		!strings.Contains(err.Error(), legacyPath) ||
		!strings.Contains(err.Error(), StateDBPath(stateDir)) ||
		!strings.Contains(err.Error(), "mv ") {
		t.Fatalf("RequireStateDBReady() error = %q, want manual balda.db rename guidance", err)
	}
}

func TestRequireStateDBReady_RejectsLegacyRelayDBOnly(t *testing.T) {
	stateDir := t.TempDir()
	legacyPath := filepath.Join(stateDir, "relay.db")
	writeTestFile(t, legacyPath)

	err := RequireStateDBReady(stateDir)
	if err == nil {
		t.Fatal("RequireStateDBReady() error = nil, want legacy database error")
	}
	if !strings.Contains(err.Error(), "legacy state database") ||
		!strings.Contains(err.Error(), legacyPath) ||
		!strings.Contains(err.Error(), StateDBPath(stateDir)) ||
		!strings.Contains(err.Error(), "cp ") {
		t.Fatalf("RequireStateDBReady() error = %q, want manual relay.db copy guidance", err)
	}
}

func TestResolveWorkingDir_EmptyUsesProcessCWD(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got, err := ResolveWorkingDir("")
	if err != nil {
		t.Fatalf("ResolveWorkingDir returned error: %v", err)
	}
	if got != filepath.Clean(cwd) {
		t.Fatalf("ResolveWorkingDir(\"\") = %q, want %q", got, filepath.Clean(cwd))
	}
}

func TestResolveWorkingDir_RelativeBecomesAbsolute(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}

	got, err := ResolveWorkingDir(".")
	if err != nil {
		t.Fatalf("ResolveWorkingDir returned error: %v", err)
	}
	if got != filepath.Clean(cwd) {
		t.Fatalf("ResolveWorkingDir(\".\") = %q, want %q", got, filepath.Clean(cwd))
	}
}

func TestResolveStateDir_RelativeUsesWorkingDir(t *testing.T) {
	workingDir := "/tmp/norma-balda-work"

	got, err := ResolveStateDir(workingDir, ".config/balda")
	if err != nil {
		t.Fatalf("ResolveStateDir returned error: %v", err)
	}

	want, err := filepath.Abs(filepath.Join(workingDir, ".config/balda"))
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	if got != filepath.Clean(want) {
		t.Fatalf("ResolveStateDir() = %q, want %q", got, filepath.Clean(want))
	}
}

func TestResolveStateDir_RequiresValue(t *testing.T) {
	if _, err := ResolveStateDir("/tmp/norma-balda-work", ""); err == nil {
		t.Fatal("ResolveStateDir returned nil error for empty state_dir")
	}
}

func writeTestFile(t *testing.T, path string) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
