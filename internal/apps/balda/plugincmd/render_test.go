package plugincmd

import (
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/pluginapp"
)

func TestRenderInstalledPluginsPlain(t *testing.T) {
	got := RenderInstalledPluginsPlain([]pluginapp.PluginSummary{{
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
	got := RenderAvailablePluginsMarkdown([]pluginapp.AvailablePlugin{{
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
	got := RenderInstalledPluginMarkdown(pluginapp.PluginSummary{
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
	got := RenderMarketplaceStatusesPlain([]pluginapp.MarketplaceStatus{{
		Name:             "main",
		Source:           "file:///tmp/main",
		Kind:             "git",
		Ref:              "main",
		CachePath:        "/state/plugins/main/repo",
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
		"cache: /state/plugins/main/repo",
		"resolved: abc123",
		"refreshed: 2026-08-09T10:00:00Z",
		"plugins: 2",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("RenderMarketplaceStatusesPlain() missing %q in %q", want, got)
		}
	}
}

func TestRenderMarketplaceUpgradeMarkdown(t *testing.T) {
	got := RenderMarketplaceUpgradeMarkdown([]pluginapp.MarketplaceUpgradeResult{{
		Name:        "main",
		PluginCount: 3,
		Status: pluginapp.MarketplaceStatus{
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
