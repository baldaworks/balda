package model_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestModelCommandUserDocs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		path string
		want []string
	}{
		{
			path: "README.md",
			want: []string{"`/model <model-id>`", "requested preference", "active observation", "same-provider", "never", "reset"},
		},
		{
			path: "docs/commands.md",
			want: []string{
				"`/model [model-id]`",
				"`/balda model [model-id]`",
				"conversation history",
				"durable requested preference",
				"valid active observation",
				"`pending`",
				"none (no explicit preference)",
				"unknown",
				"locally executed",
				"implicit reset",
				"unresolved `applying`",
				"does not repeat model validation or mutation",
				"remains `recovery_required`",
				"do not downgrade",
				"operational downgrade is prohibited",
				"must not infer requested preference",
			},
		},
		{
			path: "docs/zulip-webhook.md",
			want: []string{"`/model [model-id]`", "same-provider", "does not reset"},
		},
		{
			path: "docs/slack.md",
			want: []string{"`/balda model <model-id>`", "same-provider", "do not reset"},
		},
		{
			path: "docs/mattermost.md",
			want: []string{"`/model <model-id>`", "same-provider", "never"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			doc := readModelCommandDoc(t, tt.path)
			for _, want := range tt.want {
				if !strings.Contains(doc, want) {
					t.Errorf("%s missing %q", tt.path, want)
				}
			}
		})
	}
}

func TestModelCommandUserDocLinksResolve(t *testing.T) {
	t.Parallel()

	linkPattern := regexp.MustCompile(`\[[^]]+\]\(([^)]+)\)`)
	for _, path := range []string{"README.md", "docs/commands.md", "docs/zulip-webhook.md", "docs/slack.md", "docs/mattermost.md"} {
		doc := readModelCommandDoc(t, path)
		for _, match := range linkPattern.FindAllStringSubmatch(doc, -1) {
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

func readModelCommandDoc(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join("../../../../../..", path))
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(content)
}
