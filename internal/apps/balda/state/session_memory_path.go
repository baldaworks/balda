package state

import (
	"path/filepath"
	"strings"
)

const sessionMemoryCanonicalDirName = "session-memory.badger"

// SessionMemoryCanonicalPath returns the state-owned path for the canonical
// session-memory directory. Composition roots use this instead of inventing a
// backend path in application code.
func SessionMemoryCanonicalPath(stateDir string) string {
	return filepath.Join(strings.TrimSpace(stateDir), sessionMemoryCanonicalDirName)
}
