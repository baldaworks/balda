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

var legacyStateDBFileNames = []string{"balda.db", "relay.db"}

// ConfigDir returns the balda config directory path for the given working dir.
func ConfigDir(workingDir string) string {
	trimmed := strings.TrimSpace(workingDir)
	if trimmed == "" {
		return configDirName
	}
	return filepath.Join(trimmed, ".config", "balda")
}

// ConfigPath returns the balda config file path for the given working dir.
func ConfigPath(workingDir string) string {
	return filepath.Join(ConfigDir(workingDir), configFileName)
}

// StateDBPath returns the canonical Balda SQLite state database path.
func StateDBPath(stateDir string) string {
	return filepath.Join(stateDir, StateDBFileName)
}

// RequireStateDBReady rejects legacy-only state database filenames before a
// new empty state.db can be created.
func RequireStateDBReady(stateDir string) error {
	statePath := StateDBPath(stateDir)
	if _, err := os.Stat(statePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat balda state db %q: %w", statePath, err)
	}

	for _, legacyName := range legacyStateDBFileNames {
		legacyPath := filepath.Join(stateDir, legacyName)
		if _, err := os.Stat(legacyPath); err == nil {
			return fmt.Errorf("legacy state database %q found but Balda now uses %q; move or copy it manually before starting, for example: %s", legacyPath, statePath, legacyStateDBMigrationCommand(legacyName, legacyPath, statePath))
		} else if !os.IsNotExist(err) {
			return fmt.Errorf("stat legacy state db %q: %w", legacyPath, err)
		}
	}
	return nil
}

func legacyStateDBMigrationCommand(legacyName, legacyPath, statePath string) string {
	if legacyName == "relay.db" {
		return fmt.Sprintf("cp %q %q", legacyPath, statePath)
	}
	return fmt.Sprintf("mv %q %q", legacyPath, statePath)
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
