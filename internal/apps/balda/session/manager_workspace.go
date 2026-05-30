package session

import (
	"fmt"
	"strings"
)

func (m *Manager) sessionBranchName(sessionID string) string {
	return fmt.Sprintf("norma/balda/%s", sessionID)
}

func mergeUniqueStringIDs(base, extra []string) []string {
	if len(extra) == 0 {
		return append([]string(nil), base...)
	}

	out := make([]string, 0, len(base)+len(extra))
	seen := make(map[string]struct{}, len(base)+len(extra))
	appendUnique := func(raw string) {
		id := strings.TrimSpace(raw)
		if id == "" {
			return
		}
		if _, exists := seen[id]; exists {
			return
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}

	for _, id := range base {
		appendUnique(id)
	}
	for _, id := range extra {
		appendUnique(id)
	}

	return out
}
