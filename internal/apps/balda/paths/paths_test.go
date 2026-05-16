package paths

import (
	"os"
	"path/filepath"
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
