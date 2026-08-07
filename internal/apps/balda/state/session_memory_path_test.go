package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSessionMemoryPaths_GroupBackendsUnderStateRoot(t *testing.T) {
	stateDir := filepath.Join("/tmp", "balda-state")
	paths := SessionMemoryPaths("  " + stateDir + "  ")

	wantRoot := filepath.Join(stateDir, "session-memory")
	if paths.Root != wantRoot {
		t.Fatalf("SessionMemoryPaths().Root = %q, want %q", paths.Root, wantRoot)
	}
	if paths.Canonical != filepath.Join(wantRoot, "badger") {
		t.Fatalf("SessionMemoryPaths().Canonical = %q, want grouped badger path", paths.Canonical)
	}
	if paths.Projection != filepath.Join(wantRoot, "bleve") {
		t.Fatalf("SessionMemoryPaths().Projection = %q, want grouped bleve path", paths.Projection)
	}
	if paths.Canonical == paths.Projection {
		t.Fatal("canonical and projection paths must remain distinct")
	}
	if got := SessionMemoryRootPath(stateDir); got != paths.Root {
		t.Fatalf("SessionMemoryRootPath() = %q, want %q", got, paths.Root)
	}
	if got := SessionMemoryCanonicalPath(stateDir); got != paths.Canonical {
		t.Fatalf("SessionMemoryCanonicalPath() = %q, want %q", got, paths.Canonical)
	}
	if got := SessionMemoryProjectionPath(stateDir); got != paths.Projection {
		t.Fatalf("SessionMemoryProjectionPath() = %q, want %q", got, paths.Projection)
	}
}

func TestMigrateSessionMemoryLayout_MovesDirectoriesAndIsIdempotent(t *testing.T) {
	stateDir := t.TempDir()
	legacyCanonical := filepath.Join(stateDir, legacySessionMemoryCanonicalName)
	legacyProjection := filepath.Join(stateDir, legacySessionMemoryProjectionName)
	if err := os.MkdirAll(legacyCanonical, 0o700); err != nil {
		t.Fatalf("create legacy canonical directory: %v", err)
	}
	if err := os.MkdirAll(legacyProjection, 0o700); err != nil {
		t.Fatalf("create legacy projection directory: %v", err)
	}
	canonicalMarker := filepath.Join(legacyCanonical, "canonical-marker")
	projectionMarker := filepath.Join(legacyProjection, "projection-marker")
	if err := os.WriteFile(canonicalMarker, []byte("canonical"), 0o600); err != nil {
		t.Fatalf("write canonical marker: %v", err)
	}
	if err := os.WriteFile(projectionMarker, []byte("projection"), 0o600); err != nil {
		t.Fatalf("write projection marker: %v", err)
	}

	if err := MigrateSessionMemoryLayout(stateDir); err != nil {
		t.Fatalf("MigrateSessionMemoryLayout() error = %v", err)
	}
	paths := SessionMemoryPaths(stateDir)
	assertPathMissing(t, legacyCanonical)
	assertPathMissing(t, legacyProjection)
	assertFileContents(t, filepath.Join(paths.Canonical, "canonical-marker"), "canonical")
	assertFileContents(t, filepath.Join(paths.Projection, "projection-marker"), "projection")
	if info, err := os.Stat(paths.Root); err != nil {
		t.Fatalf("stat grouped root: %v", err)
	} else if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("grouped root permissions = %o, want 700", got)
	}

	if err := MigrateSessionMemoryLayout(stateDir); err != nil {
		t.Fatalf("second MigrateSessionMemoryLayout() error = %v", err)
	}
	assertFileContents(t, filepath.Join(paths.Canonical, "canonical-marker"), "canonical")
}

func TestMigrateSessionMemoryLayout_FreshStateDoesNotCreateRoot(t *testing.T) {
	stateDir := t.TempDir()
	if err := MigrateSessionMemoryLayout(stateDir); err != nil {
		t.Fatalf("MigrateSessionMemoryLayout() error = %v", err)
	}
	assertPathMissing(t, SessionMemoryRootPath(stateDir))

	paths := SessionMemoryPaths(stateDir)
	if err := os.MkdirAll(paths.Canonical, 0o700); err != nil {
		t.Fatalf("create grouped canonical directory: %v", err)
	}
	if err := MigrateSessionMemoryLayout(stateDir); err != nil {
		t.Fatalf("target-only MigrateSessionMemoryLayout() error = %v", err)
	}
	if _, err := os.Stat(paths.Canonical); err != nil {
		t.Fatalf("grouped canonical directory after target-only migration: %v", err)
	}
}

func TestMigrateSessionMemoryLayout_ConflictsFailClosed(t *testing.T) {
	stateDir := t.TempDir()
	paths := SessionMemoryPaths(stateDir)
	legacyCanonical := filepath.Join(stateDir, legacySessionMemoryCanonicalName)
	if err := os.MkdirAll(legacyCanonical, 0o700); err != nil {
		t.Fatalf("create legacy canonical directory: %v", err)
	}
	if err := os.MkdirAll(paths.Canonical, 0o700); err != nil {
		t.Fatalf("create grouped canonical directory: %v", err)
	}

	err := MigrateSessionMemoryLayout(stateDir)
	if err == nil {
		t.Fatal("MigrateSessionMemoryLayout() error = nil, want conflict")
	}
	if !strings.Contains(err.Error(), "old and grouped canonical paths both exist") {
		t.Fatalf("MigrateSessionMemoryLayout() error = %v, want canonical conflict", err)
	}
	if _, err := os.Stat(legacyCanonical); err != nil {
		t.Fatalf("legacy canonical directory after conflict: %v", err)
	}
	if _, err := os.Stat(paths.Canonical); err != nil {
		t.Fatalf("grouped canonical directory after conflict: %v", err)
	}
}

func TestMigrateSessionMemoryLayout_InvalidLegacyMetadataFailsClosed(t *testing.T) {
	stateDir := t.TempDir()
	legacyCanonical := filepath.Join(stateDir, legacySessionMemoryCanonicalName)
	if err := os.WriteFile(legacyCanonical, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("write invalid legacy canonical path: %v", err)
	}

	err := MigrateSessionMemoryLayout(stateDir)
	if err == nil {
		t.Fatal("MigrateSessionMemoryLayout() error = nil, want invalid metadata error")
	}
	if !strings.Contains(err.Error(), "old canonical path") || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("MigrateSessionMemoryLayout() error = %v, want invalid canonical metadata", err)
	}
	assertPathMissing(t, SessionMemoryCanonicalPath(stateDir))
}

func TestMigrateSessionMemoryLayout_SymbolicLinkFailsClosed(t *testing.T) {
	for _, withTarget := range []bool{false, true} {
		name := "missing-leaf"
		if withTarget {
			name = "existing-leaf"
		}
		t.Run(name, func(t *testing.T) {
			stateDir := t.TempDir()
			outsideDir := t.TempDir()
			root := SessionMemoryRootPath(stateDir)
			if withTarget {
				if err := os.MkdirAll(filepath.Join(outsideDir, "badger"), 0o700); err != nil {
					t.Fatalf("create outside grouped leaf: %v", err)
				}
			}
			if err := os.Symlink(outsideDir, root); err != nil {
				t.Skipf("symbolic links are unavailable: %v", err)
			}
			legacyCanonical := filepath.Join(stateDir, legacySessionMemoryCanonicalName)
			if err := os.MkdirAll(legacyCanonical, 0o700); err != nil {
				t.Fatalf("create legacy canonical directory: %v", err)
			}

			err := MigrateSessionMemoryLayout(stateDir)
			if err == nil {
				t.Fatal("MigrateSessionMemoryLayout() error = nil, want symbolic-link error")
			}
			if !strings.Contains(err.Error(), "symbolic link") {
				t.Fatalf("MigrateSessionMemoryLayout() error = %v, want symbolic-link error", err)
			}
			outsideCanonical := filepath.Join(outsideDir, "badger")
			if withTarget {
				if _, err := os.Stat(outsideCanonical); err != nil {
					t.Fatalf("outside grouped leaf after symbolic-link rejection: %v", err)
				}
			} else {
				assertPathMissing(t, outsideCanonical)
			}
			if _, err := os.Stat(legacyCanonical); err != nil {
				t.Fatalf("legacy canonical directory after symbolic-link rejection: %v", err)
			}
		})
	}
}

func assertPathMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("stat %q error = %v, want not exist", path, err)
	}
}

func assertFileContents(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	if string(got) != want {
		t.Fatalf("contents of %q = %q, want %q", path, got, want)
	}
}
