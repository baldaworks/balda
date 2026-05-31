package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	configDirName  = ".config/balda"
	configFileName = "config.yaml"

	// StateDBFileName is the canonical Balda SQLite state database filename.
	StateDBFileName = "state.db"
)

// ConfigPath returns the balda config file path for the given working dir.
func ConfigPath(workingDir string) string {
	trimmed := strings.TrimSpace(workingDir)
	if trimmed == "" {
		return filepath.Join(configDirName, configFileName)
	}
	return filepath.Join(trimmed, ".config", "balda", configFileName)
}

// StateDBPath returns the canonical Balda SQLite state database path.
func StateDBPath(stateDir string) string {
	return filepath.Join(stateDir, StateDBFileName)
}

// ResolveWorkingDir returns an absolute clean working directory path.
func ResolveWorkingDir(raw string) (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("get current working directory: %w", err)
	}

	workingDir := strings.TrimSpace(raw)
	if workingDir == "" {
		return filepath.Clean(cwd), nil
	}
	if !filepath.IsAbs(workingDir) {
		workingDir = filepath.Join(cwd, workingDir)
	}

	resolved, err := filepath.Abs(workingDir)
	if err != nil {
		return "", fmt.Errorf("resolve absolute working_dir %q: %w", raw, err)
	}
	return filepath.Clean(resolved), nil
}

// ResolveStateDir returns an absolute clean balda state directory path.
func ResolveStateDir(workingDir, rawStateDir string) (string, error) {
	stateDir := strings.TrimSpace(rawStateDir)
	if stateDir == "" {
		return "", fmt.Errorf("balda.state_dir is required")
	}
	if !filepath.IsAbs(stateDir) {
		stateDir = filepath.Join(workingDir, stateDir)
	}

	resolved, err := filepath.Abs(stateDir)
	if err != nil {
		return "", fmt.Errorf("resolve balda state_dir %q: %w", rawStateDir, err)
	}
	return filepath.Clean(resolved), nil
}
