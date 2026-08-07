package state

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	sessionMemoryRootDirName          = "session-memory"
	sessionMemoryCanonicalDirName     = "badger"
	sessionMemoryProjectionDirName    = "bleve"
	legacySessionMemoryCanonicalName  = "session-memory.badger"
	legacySessionMemoryProjectionName = "session-memory-bleve"
)

// SessionMemoryPathSet contains all Balda-owned filesystem roots for the
// portable session-memory runtime. The canonical and projection paths are
// intentionally separate children of Root.
type SessionMemoryPathSet struct {
	Root       string
	Canonical  string
	Projection string
}

// SessionMemoryPaths returns the state-owned session-memory layout for a
// resolved Balda state directory.
func SessionMemoryPaths(stateDir string) SessionMemoryPathSet {
	trimmed := strings.TrimSpace(stateDir)
	root := filepath.Join(trimmed, sessionMemoryRootDirName)
	return SessionMemoryPathSet{
		Root:       root,
		Canonical:  filepath.Join(root, sessionMemoryCanonicalDirName),
		Projection: filepath.Join(root, sessionMemoryProjectionDirName),
	}
}

// SessionMemoryRootPath returns the state-owned root for session-memory
// backend artifacts.
func SessionMemoryRootPath(stateDir string) string {
	return SessionMemoryPaths(stateDir).Root
}

// SessionMemoryCanonicalPath returns the state-owned path for the canonical
// session-memory directory. Composition roots use this instead of inventing a
// backend path in application code.
func SessionMemoryCanonicalPath(stateDir string) string {
	return SessionMemoryPaths(stateDir).Canonical
}

// SessionMemoryProjectionPath returns the disposable Bleve generation root.
// It is separate from the canonical Badger owner and can be rebuilt without
// changing logical memory state.
func SessionMemoryProjectionPath(stateDir string) string {
	return SessionMemoryPaths(stateDir).Projection
}

// MigrateSessionMemoryLayout relocates the previous direct-child session-memory
// directories into the grouped state-owned layout before a backend is opened.
// It only performs same-filesystem renames, so canonical records are not
// decoded, copied, or rewritten. The operation is idempotent after a
// successful move and fails closed when old and new locations both exist.
func MigrateSessionMemoryLayout(stateDir string) error {
	trimmed := strings.TrimSpace(stateDir)
	if trimmed == "" {
		return errors.New("session-memory state directory is required")
	}

	paths := SessionMemoryPaths(trimmed)
	if err := validateNoSymlinkPath(paths.Root); err != nil {
		return fmt.Errorf("validate session-memory path root: %w", err)
	}
	legacyCanonical := filepath.Join(trimmed, legacySessionMemoryCanonicalName)
	legacyProjection := filepath.Join(trimmed, legacySessionMemoryProjectionName)

	if err := migrateSessionMemoryDirectory("canonical", legacyCanonical, paths.Canonical, paths.Root); err != nil {
		return fmt.Errorf("migrate canonical session-memory directory: %w", err)
	}
	if err := migrateSessionMemoryDirectory("projection", legacyProjection, paths.Projection, paths.Root); err != nil {
		return fmt.Errorf("migrate projection session-memory directory: %w", err)
	}
	return nil
}

func migrateSessionMemoryDirectory(kind, oldPath, newPath, root string) error {
	oldExists, oldInfo, err := sessionMemoryDirectoryInfo(oldPath)
	if err != nil {
		return fmt.Errorf("inspect old %s path %q: %w", kind, oldPath, err)
	}
	newExists, newInfo, err := sessionMemoryDirectoryInfo(newPath)
	if err != nil {
		return fmt.Errorf("inspect grouped %s path %q: %w", kind, newPath, err)
	}

	if oldExists && newExists {
		return fmt.Errorf("old and grouped %s paths both exist: %q and %q", kind, oldPath, newPath)
	}
	if newExists {
		if !newInfo.IsDir() {
			return fmt.Errorf("grouped %s path %q is not a directory", kind, newPath)
		}
		return nil
	}
	if !oldExists {
		return nil
	}
	if !oldInfo.IsDir() {
		return fmt.Errorf("old %s path %q is not a directory", kind, oldPath)
	}

	if err := os.MkdirAll(root, 0o700); err != nil {
		return fmt.Errorf("create grouped session-memory root %q: %w", root, err)
	}
	if err := os.Rename(oldPath, newPath); err != nil {
		return fmt.Errorf("rename %s path %q to %q: %w", kind, oldPath, newPath, err)
	}
	return nil
}

func sessionMemoryDirectoryInfo(path string) (bool, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil, nil
	}
	if err != nil {
		return false, nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return false, nil, fmt.Errorf("path %q is a symbolic link", path)
	}
	return true, info, nil
}

func validateNoSymlinkPath(path string) error {
	cleaned := filepath.Clean(path)
	for current := cleaned; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("path %q contains symbolic link component %q", path, current)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("inspect path component %q: %w", current, err)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}
