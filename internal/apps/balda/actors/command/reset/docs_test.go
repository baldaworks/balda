package reset

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestFreshLifetimeUserDocs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		path string
		want []string
	}{
		{path: "README.md", want: []string{"`/reset` and `/new`", "empty local conversation", "provider, model, and", "capabilities"}},
		{path: "docs/commands.md", want: []string{"### `/reset` and `/new`", "Both commands are bare", "local logical isolation", "does not physically delete remote", "Provider-native state is created lazily", "outcome is unconfirmed", "`/status` before retrying"}},
		{path: "docs/slack.md", want: []string{"`/balda reset` and `/balda new`", "remote-history deletion"}},
		{path: "docs/zulip-webhook.md", want: []string{"`/reset` and `/new` accept no arguments", "local logical", "remote provider history"}},
		{path: "docs/mattermost.md", want: []string{"Bare `/reset` and `/new`", "remote-history deletion"}},
	}
	for _, test := range tests {
		t.Run(test.path, func(t *testing.T) {
			doc := readFreshLifetimeDoc(t, test.path)
			for _, want := range test.want {
				if !strings.Contains(doc, want) {
					t.Errorf("%s missing %q", test.path, want)
				}
			}
		})
	}
}

func TestFreshLifetimeCoreDocsContract(t *testing.T) {
	t.Parallel()
	catalog := readFreshLifetimeDoc(t, "docs/architecture/runtime-contribution-catalog.md")
	contracts := []struct {
		name string
		text string
		want []string
	}{
		{
			name: "agent command",
			text: lineContaining(t, readFreshLifetimeDoc(t, "AGENTS.md"), "- `/reset` or `/new`:"),
			want: []string{"queues behind active and prior session work", "existing fifo", "new empty local adk lifetime/archive", "same stable balda session", "exact provider, model, and capabilities", "provider use stays lazy", "does not promise provider availability", "remote provider history"},
		},
		{
			name: "project skills",
			text: paragraphContaining(t, catalog, "Session snapshots are immutable."),
			want: []string{"existing per-session fifo", "new empty local adk lifetime/archive", "same stable balda session", "exact pinned capability snapshot", "project-skill revisions", "only a genuinely new stable session selects the current project-skill revision"},
		},
		{
			name: "catalog publication",
			text: paragraphContaining(t, catalog, "Publishing a new catalog snapshot never mutates an active session."),
			want: []string{"existing per-session fifo", "new empty local adk lifetime/archive", "same stable balda session", "exact pinned `snapshotid`", "do not select the current catalog revision", "only creation of a genuinely new stable session selects the current catalog revision"},
		},
		{
			name: "command",
			text: markdownSection(t, readFreshLifetimeDoc(t, "docs/commands.md"), "### `/reset` and `/new`"),
			want: []string{"behind active and already queued session work", "stable session and locator", "exact provider binding, effective model, and capability snapshot", "new empty local conversation/archive identity", "does not physically delete remote provider history", "provider-native state is created lazily"},
		},
	}
	for _, contract := range contracts {
		t.Run(contract.name, func(t *testing.T) {
			normalized := normalizeProse(contract.text)
			for _, want := range contract.want {
				if !strings.Contains(normalized, want) {
					t.Errorf("contract missing %q in %q", want, normalized)
				}
			}
			if stale := staleResetClaims(contract.text); len(stale) != 0 {
				t.Errorf("contract makes stale reset claims: %v", stale)
			}
		})
	}
}

func TestStaleResetClaims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		text  string
		stale bool
	}{
		{name: "wrapped latest selection", text: "`/reset` selects the latest catalog\nrevision.", stale: true},
		{name: "current selection", text: "`/new` adopts the current project-skill revision.", stale: true},
		{name: "active cancellation", text: "`/reset` cancels active work.", stale: true},
		{name: "negated cancellation", text: "`/reset` does not cancel active work."},
		{name: "negated selection", text: "`/reset` and `/new` do not select the current catalog revision."},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := len(staleResetClaims(test.text)) != 0
			if got != test.stale {
				t.Errorf("staleResetClaims(%q) = %v, want %v", test.text, got, test.stale)
			}
		})
	}
}

func TestFreshLifetimeUserDocLinksResolve(t *testing.T) {
	t.Parallel()
	pattern := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	for _, path := range []string{"README.md", "docs/commands.md", "docs/slack.md", "docs/zulip-webhook.md", "docs/mattermost.md"} {
		doc := readFreshLifetimeDoc(t, path)
		for _, match := range pattern.FindAllStringSubmatch(doc, -1) {
			target := match[1]
			if strings.HasPrefix(target, "#") || strings.Contains(target, "://") || strings.HasPrefix(target, "mailto:") {
				continue
			}
			target, _, _ = strings.Cut(target, "#")
			resolved := filepath.Join("../../../../../..", filepath.Dir(path), target)
			if _, err := os.Stat(resolved); err != nil {
				t.Errorf("%s link %q does not resolve: %v", path, match[1], err)
			}
		}
	}
}

func readFreshLifetimeDoc(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("../../../../../..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}

func paragraphContaining(t *testing.T, doc, marker string) string {
	t.Helper()
	for _, paragraph := range strings.Split(doc, "\n\n") {
		if strings.Contains(paragraph, marker) {
			return paragraph
		}
	}
	t.Fatalf("paragraph containing %q not found", marker)
	return ""
}

func lineContaining(t *testing.T, doc, marker string) string {
	t.Helper()
	for _, line := range strings.Split(doc, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	t.Fatalf("line containing %q not found", marker)
	return ""
}

func markdownSection(t *testing.T, doc, heading string) string {
	t.Helper()
	start := strings.Index(doc, heading)
	if start < 0 {
		t.Fatalf("section %q not found", heading)
	}
	section := doc[start:]
	if end := strings.Index(section[len(heading):], "\n### "); end >= 0 {
		section = section[:len(heading)+end]
	}
	return section
}

func normalizeProse(text string) string {
	return strings.ToLower(strings.Join(strings.Fields(text), " "))
}

func staleResetClaims(text string) []string {
	text = normalizeProse(text)
	clauses := strings.FieldsFunc(text, func(r rune) bool {
		return r == '.' || r == '!' || r == '?'
	})
	var stale []string
	for _, clause := range clauses {
		if !strings.Contains(clause, "/reset") && !strings.Contains(clause, "/new") {
			continue
		}
		if strings.Contains(clause, "immediate") || strings.Contains(clause, "unpinned replacement") || strings.Contains(clause, "closes the old session runtime") {
			stale = append(stale, clause)
			continue
		}
		if claimsAction(clause, []string{"select", "selects", "adopt", "adopts", "capture", "captures"}, []string{"current revision", "latest revision", "current catalog revision", "latest catalog revision", "current project-skill revision", "latest project-skill revision"}) ||
			claimsAction(clause, []string{"cancel", "cancels"}, []string{"active work", "current work", "session work"}) ||
			claimsAction(clause, []string{"clear", "clears"}, []string{"history", "context"}) {
			stale = append(stale, clause)
		}
	}
	return stale
}

func claimsAction(clause string, verbs, objects []string) bool {
	verb := ""
	for _, candidate := range verbs {
		if strings.Contains(clause, candidate) {
			verb = candidate
			break
		}
	}
	if verb == "" {
		return false
	}
	objectFound := false
	for _, object := range objects {
		if strings.Contains(clause, object) {
			objectFound = true
			break
		}
	}
	if !objectFound {
		return false
	}
	for _, negation := range []string{"do not ", "does not ", "never "} {
		if strings.Contains(clause, negation+verb) {
			return false
		}
	}
	return true
}
