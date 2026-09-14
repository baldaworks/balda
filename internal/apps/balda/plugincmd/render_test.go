package plugincmd

import (
	"strings"
	"testing"
)

func TestRenderInstalledPluginsPlain(t *testing.T) {
	got := RenderInstalledPluginsPlain([]PluginSummary{{
		Name:        "demo",
		Version:     "1.2.3",
		Description: "Demo plugin",
	}})
	want := "Installed plugins:\n- demo 1.2.3\n  Demo plugin"
	if got != want {
		t.Fatalf("RenderInstalledPluginsPlain() = %q, want %q", got, want)
	}
}

func TestRenderAvailablePluginsMarkdown(t *testing.T) {
	got := RenderAvailablePluginsMarkdown([]AvailablePlugin{{
		Name:        "demo",
		Marketplace: "main",
		Version:     "1.2.3",
		Description: "Demo plugin",
		Category:    "Utilities",
		Installed:   true,
	}})
	for _, want := range []string{
		"# Available plugins",
		"`demo@main` 1.2.3",
		"category: Utilities",
		"installed: yes",
		"Demo plugin",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderAvailablePluginsMarkdown() missing %q in %q", want, got)
		}
	}
}

func TestRenderInstalledPluginMarkdown(t *testing.T) {
	got := RenderInstalledPluginMarkdown(PluginSummary{
		Name:        "demo",
		Version:     "1.2.3",
		Description: "Demo plugin",
	})
	for _, want := range []string{
		"# Plugin",
		"**Name:** `demo`",
		"**Version:** `1.2.3`",
		"**Description:** Demo plugin",
		"**Installed:** yes",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderInstalledPluginMarkdown() missing %q in %q", want, got)
		}
	}
}

func TestRenderMarketplaceStatusesPlain(t *testing.T) {
	got := RenderMarketplaceStatusesPlain([]MarketplaceStatus{{
		Name:             "main",
		Source:           "file:///tmp/main",
		Kind:             "git",
		Ref:              "main",
		ResolvedRef:      "abc123",
		LastRefreshedAt:  "2026-08-09T10:00:00Z",
		ManifestPresent:  true,
		AvailablePlugins: 2,
	}})
	for _, want := range []string{
		"Configured plugin marketplaces:",
		"- main",
		"source: file:///tmp/main",
		"kind: git",
		"ref: main",
		"resolved: abc123",
		"refreshed: 2026-08-09T10:00:00Z",
		"plugins: 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderMarketplaceStatusesPlain() missing %q in %q", want, got)
		}
	}
}

func TestPublicMarketplaceSourceRedactsHostAndCredentialData(t *testing.T) {
	tests := map[string]string{
		"/srv/private/plugins":                          "local",
		"file:///srv/private/plugins":                   "local",
		"https://user:secret@example.test/repo?q=token": "https://example.test/repo",
		"https://user:secret@example.test/repo%zz":      "redacted",
		"github.com/baldaworks/plugins?token=secret":    "github.com/baldaworks/plugins",
		`C:\private\plugins`:                            "local",
		"github.com/baldaworks/plugins":                 "github.com/baldaworks/plugins",
	}
	for source, want := range tests {
		if got := PublicMarketplaceSource(source); got != want {
			t.Errorf("PublicMarketplaceSource(%q) = %q, want %q", source, got, want)
		}
	}
}

func TestRenderMarketplaceUpgradeMarkdown(t *testing.T) {
	got := RenderMarketplaceUpgradeMarkdown([]MarketplaceUpgradeResult{{
		Name:        "main",
		PluginCount: 3,
		Status: MarketplaceStatus{
			ResolvedRef:     "deadbeef",
			LastRefreshedAt: "2026-08-09T10:01:00Z",
		},
	}})
	for _, want := range []string{
		"# Plugin marketplaces refreshed",
		"`main` — 3 plugins",
		"resolved: `deadbeef`",
		"refreshed: `2026-08-09T10:01:00Z`",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderMarketplaceUpgradeMarkdown() missing %q in %q", want, got)
		}
	}
}

func TestRenderPluginStatusMarkdownExcludesSecretRichData(t *testing.T) {
	status := PluginStatus{
		Name: "demo", Version: "1.2.3", Marketplace: "official", Origin: "plugins/demo",
		Revision: "sha256-revision", Enabled: true,
		Capabilities: CapabilitySummary{Commands: 1, Skills: 2, MCPServers: 1, Diagnostics: 3},
		Runtime: RuntimeStatus{
			SnapshotID: "snapshot", SnapshotSequence: 7, ProjectionLag: 1,
			Advertisements: 1, Skills: 2, SkillAmbiguities: 1,
			DesiredMCPServers: 1, ReadyMCPServers: 0, DegradedMCPServers: 1,
			DiagnosticCodes: []string{"mcp_config_invalid"}, ProjectionOmissions: []string{"projection"},
		},
	}
	got := RenderPluginStatusMarkdown(status)
	for _, want := range []string{"sha256-revision", "snapshot", "commands=1", "desired=1", "Projection lag", "mcp_config_invalid"} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderPluginStatusMarkdown() missing %q in %q", want, got)
		}
	}
	for _, forbidden := range []string{"/home/", "Authorization:", "SECRET=", "SKILL.md body"} {
		if strings.Contains(got, forbidden) {
			t.Fatalf("RenderPluginStatusMarkdown() leaked %q in %q", forbidden, got)
		}
	}
}
