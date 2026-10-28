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
