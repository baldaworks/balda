package plugincmd

import (
	"fmt"
	"strings"
)

func RenderMarketplaceStatusesPlain(statuses []MarketplaceStatus) string {
	if len(statuses) == 0 {
		return "No configured plugin marketplaces."
	}
	lines := []string{"Configured plugin marketplaces:"}
	for _, src := range statuses {
		lines = append(lines, "- "+src.Name)
		lines = append(lines, "  source: "+src.Source)
		if src.Kind != "" {
			lines = append(lines, "  kind: "+src.Kind)
		}
		if src.Ref != "" {
			lines = append(lines, "  ref: "+src.Ref)
		}
		if len(src.Sparse) > 0 {
			lines = append(lines, "  sparse: "+strings.Join(src.Sparse, ", "))
		}
		if src.ResolvedRef != "" {
			lines = append(lines, "  resolved: "+src.ResolvedRef)
		}
		if src.LastRefreshedAt != "" {
			lines = append(lines, "  refreshed: "+src.LastRefreshedAt)
		}
		if src.ManifestPresent {
			lines = append(lines, fmt.Sprintf("  plugins: %d", src.AvailablePlugins))
		}
	}
	return strings.Join(lines, "\n")
}

func RenderMarketplaceStatusesMarkdown(statuses []MarketplaceStatus) string {
	if len(statuses) == 0 {
		return "# Plugin marketplaces\n\nNo configured plugin marketplaces."
	}
	lines := []string{"# Plugin marketplaces", ""}
	for _, src := range statuses {
		lines = append(lines, "- `"+src.Name+"`")
		lines = append(lines, "  source: `"+src.Source+"`")
		if src.Kind != "" {
			lines = append(lines, "  kind: `"+src.Kind+"`")
		}
		if src.Ref != "" {
			lines = append(lines, "  ref: `"+src.Ref+"`")
		}
		if len(src.Sparse) > 0 {
			lines = append(lines, "  sparse: `"+strings.Join(src.Sparse, ", ")+"`")
		}
		if src.ResolvedRef != "" {
			lines = append(lines, "  resolved: `"+src.ResolvedRef+"`")
		}
		if src.LastRefreshedAt != "" {
			lines = append(lines, "  refreshed: `"+src.LastRefreshedAt+"`")
		}
		if src.ManifestPresent {
			lines = append(lines, fmt.Sprintf("  plugins: `%d`", src.AvailablePlugins))
		}
	}
	return strings.Join(lines, "\n")
}

func RenderMarketplaceStatusPlain(status MarketplaceStatus) string {
	lines := []string{"Plugin marketplace:", "- name: " + status.Name, "- source: " + status.Source}
	if status.Kind != "" {
		lines = append(lines, "- kind: "+status.Kind)
	}
	if status.Ref != "" {
		lines = append(lines, "- ref: "+status.Ref)
	}
	if len(status.Sparse) > 0 {
		lines = append(lines, "  sparse: "+strings.Join(status.Sparse, ", "))
	}
	if status.ResolvedRef != "" {
		lines = append(lines, "- resolved: "+status.ResolvedRef)
	}
	if status.LastRefreshedAt != "" {
		lines = append(lines, "- refreshed: "+status.LastRefreshedAt)
	}
	lines = append(lines, fmt.Sprintf("- manifest present: %t", status.ManifestPresent))
	if status.ManifestPresent {
		lines = append(lines, fmt.Sprintf("- plugins: %d", status.AvailablePlugins))
	}
	return strings.Join(lines, "\n")
}

func RenderMarketplaceStatusMarkdown(status MarketplaceStatus) string {
	lines := []string{"# Plugin marketplace", "", "- **Name:** `" + status.Name + "`", "- **Source:** `" + status.Source + "`"}
	if status.Kind != "" {
		lines = append(lines, "- **Kind:** `"+status.Kind+"`")
	}
	if status.Ref != "" {
		lines = append(lines, "- **Ref:** `"+status.Ref+"`")
	}
	if len(status.Sparse) > 0 {
		lines = append(lines, "- **Sparse:** `"+strings.Join(status.Sparse, ", ")+"`")
	}
	if status.ResolvedRef != "" {
		lines = append(lines, "- **Resolved:** `"+status.ResolvedRef+"`")
	}
	if status.LastRefreshedAt != "" {
		lines = append(lines, "- **Refreshed:** `"+status.LastRefreshedAt+"`")
	}
	lines = append(lines, fmt.Sprintf("- **Manifest present:** `%t`", status.ManifestPresent))
	if status.ManifestPresent {
		lines = append(lines, fmt.Sprintf("- **Plugins:** `%d`", status.AvailablePlugins))
	}
	return strings.Join(lines, "\n")
}

func RenderMarketplaceUpgradePlain(results []MarketplaceUpgradeResult) string {
	if len(results) == 0 {
		return "No configured plugin marketplaces."
	}
	lines := []string{"Marketplace refresh complete:"}
	for _, result := range results {
		line := "- " + result.Name
		if result.PluginCount > 0 {
			line += fmt.Sprintf(" (%d plugins)", result.PluginCount)
		}
		lines = append(lines, line)
		if result.Status.ResolvedRef != "" {
			lines = append(lines, "  resolved: "+result.Status.ResolvedRef)
		}
		if result.Status.LastRefreshedAt != "" {
			lines = append(lines, "  refreshed: "+result.Status.LastRefreshedAt)
		}
	}
	return strings.Join(lines, "\n")
}

func RenderMarketplaceUpgradeMarkdown(results []MarketplaceUpgradeResult) string {
	if len(results) == 0 {
		return "# Plugin marketplaces\n\nNo configured plugin marketplaces."
	}
	lines := []string{"# Plugin marketplaces refreshed", ""}
	for _, result := range results {
		line := "- `" + result.Name + "`"
		if result.PluginCount > 0 {
			line += fmt.Sprintf(" — %d plugins", result.PluginCount)
		}
		lines = append(lines, line)
		if result.Status.ResolvedRef != "" {
			lines = append(lines, "  resolved: `"+result.Status.ResolvedRef+"`")
		}
		if result.Status.LastRefreshedAt != "" {
			lines = append(lines, "  refreshed: `"+result.Status.LastRefreshedAt+"`")
		}
	}
	return strings.Join(lines, "\n")
}

func RenderInstalledPluginsPlain(plugins []PluginSummary) string {
	if len(plugins) == 0 {
		return "No installed plugins."
	}
	lines := []string{"Installed plugins:"}
	for _, plugin := range plugins {
		line := "- " + plugin.Name
		if strings.TrimSpace(plugin.Version) != "" {
			line += " " + plugin.Version
		}
		lines = append(lines, line)
		if desc := strings.TrimSpace(plugin.Description); desc != "" {
			lines = append(lines, "  "+desc)
		}
	}
	return strings.Join(lines, "\n")
}

func RenderInstalledPluginsMarkdown(plugins []PluginSummary) string {
	if len(plugins) == 0 {
		return "# Installed plugins\n\nNo installed plugins."
	}
	lines := []string{"# Installed plugins", ""}
	for _, plugin := range plugins {
		line := "- `" + plugin.Name + "`"
		if strings.TrimSpace(plugin.Version) != "" {
			line += " " + plugin.Version
		}
		lines = append(lines, line)
		if desc := strings.TrimSpace(plugin.Description); desc != "" {
			lines = append(lines, "  "+desc)
		}
	}
	return strings.Join(lines, "\n")
}

func RenderAvailablePluginsPlain(plugins []AvailablePlugin) string {
	if len(plugins) == 0 {
		return "No available plugins."
	}
	lines := []string{"Available plugins:"}
	for _, plugin := range plugins {
		line := "- " + plugin.Name + "@" + plugin.Marketplace
		if strings.TrimSpace(plugin.Version) != "" {
			line += " " + plugin.Version
		}
		lines = append(lines, line)
		if plugin.Category != "" {
			lines = append(lines, "  category: "+plugin.Category)
		}
		if plugin.Installed {
			lines = append(lines, "  installed: yes")
		}
		if desc := strings.TrimSpace(plugin.Description); desc != "" {
			lines = append(lines, "  "+desc)
		}
	}
	return strings.Join(lines, "\n")
}

func RenderAvailablePluginsMarkdown(plugins []AvailablePlugin) string {
	if len(plugins) == 0 {
		return "# Available plugins\n\nNo available plugins."
	}
	lines := []string{"# Available plugins", ""}
	for _, plugin := range plugins {
		line := "- `" + plugin.Name + "@" + plugin.Marketplace + "`"
		if strings.TrimSpace(plugin.Version) != "" {
			line += " " + plugin.Version
		}
		lines = append(lines, line)
		if strings.TrimSpace(plugin.Category) != "" {
			lines = append(lines, "  category: "+plugin.Category)
		}
		if plugin.Installed {
			lines = append(lines, "  installed: yes")
		}
		if desc := strings.TrimSpace(plugin.Description); desc != "" {
			lines = append(lines, "  "+desc)
		}
	}
	return strings.Join(lines, "\n")
}

func RenderInstalledPluginPlain(plugin PluginSummary) string {
	lines := []string{"Plugin:", "- name: " + plugin.Name}
	if strings.TrimSpace(plugin.Version) != "" {
		lines = append(lines, "- version: "+plugin.Version)
	}
	if strings.TrimSpace(plugin.Description) != "" {
		lines = append(lines, "- description: "+plugin.Description)
	}
	lines = append(lines, "- installed: yes")
	return strings.Join(lines, "\n")
}

func RenderInstalledPluginMarkdown(plugin PluginSummary) string {
	lines := []string{"# Plugin", "", "- **Name:** `" + plugin.Name + "`"}
	if strings.TrimSpace(plugin.Version) != "" {
		lines = append(lines, "- **Version:** `"+plugin.Version+"`")
	}
	if strings.TrimSpace(plugin.Description) != "" {
		lines = append(lines, "- **Description:** "+plugin.Description)
	}
	lines = append(lines, "- **Installed:** yes")
	return strings.Join(lines, "\n")
}

func RenderAvailablePluginPlain(plugin AvailablePlugin) string {
	lines := []string{"Plugin:", "- name: " + plugin.Name, "- marketplace: " + plugin.Marketplace}
	if strings.TrimSpace(plugin.Version) != "" {
		lines = append(lines, "- version: "+plugin.Version)
	}
	if strings.TrimSpace(plugin.Description) != "" {
		lines = append(lines, "- description: "+plugin.Description)
	}
	if strings.TrimSpace(plugin.Category) != "" {
		lines = append(lines, "- category: "+plugin.Category)
	}
	if plugin.Installed {
		lines = append(lines, "- installed: yes")
	}
	return strings.Join(lines, "\n")
}

func RenderAvailablePluginMarkdown(plugin AvailablePlugin) string {
	lines := []string{"# Plugin", "", "- **Name:** `" + plugin.Name + "`", "- **Marketplace:** `" + plugin.Marketplace + "`"}
	if strings.TrimSpace(plugin.Version) != "" {
		lines = append(lines, "- **Version:** `"+plugin.Version+"`")
	}
	if strings.TrimSpace(plugin.Description) != "" {
		lines = append(lines, "- **Description:** "+plugin.Description)
	}
	if strings.TrimSpace(plugin.Category) != "" {
		lines = append(lines, "- **Category:** "+plugin.Category)
	}
	if plugin.Installed {
		lines = append(lines, "- **Installed:** yes")
	}
	return strings.Join(lines, "\n")
}

// RenderPluginStatusMarkdown renders only bounded, non-secret status fields.
func RenderPluginStatusMarkdown(status PluginStatus) string {
	lines := []string{
		"# Plugin status", "",
		"- **Name:** `" + status.Name + "`",
		"- **Version:** `" + status.Version + "`",
		"- **Marketplace:** `" + status.Marketplace + "`",
		"- **Origin:** `" + status.Origin + "`",
		"- **Revision:** `" + status.Revision + "`",
		fmt.Sprintf("- **Enabled:** `%t`", status.Enabled),
		fmt.Sprintf("- **Drifted:** `%t`", status.Drifted),
		fmt.Sprintf("- **Capabilities:** commands=%d skills=%d mcp=%d diagnostics=%d",
			status.Capabilities.Commands, status.Capabilities.Skills,
			status.Capabilities.MCPServers, status.Capabilities.Diagnostics),
		"", "## Runtime catalog",
		"",
		"- **Snapshot:** `" + status.Runtime.SnapshotID + "`",
		fmt.Sprintf("- **Snapshot sequence:** `%d`", status.Runtime.SnapshotSequence),
		fmt.Sprintf("- **Projection lag:** `%d`", status.Runtime.ProjectionLag),
		fmt.Sprintf("- **Advertisements:** `%d`", status.Runtime.Advertisements),
		fmt.Sprintf("- **Skills:** `%d` (ambiguous names: %d)", status.Runtime.Skills, status.Runtime.SkillAmbiguities),
		fmt.Sprintf("- **MCP:** desired=%d ready=%d degraded=%d", status.Runtime.DesiredMCPServers,
			status.Runtime.ReadyMCPServers, status.Runtime.DegradedMCPServers),
	}
	if len(status.Runtime.DiagnosticCodes) > 0 {
		lines = append(lines, "- **Diagnostics:** `"+strings.Join(status.Runtime.DiagnosticCodes, "`, `")+"`")
	}
	if len(status.Runtime.ProjectionOmissions) > 0 {
		lines = append(lines, "- **Projection omissions:** `"+strings.Join(status.Runtime.ProjectionOmissions, "`, `")+"`")
	}
	return strings.Join(lines, "\n")
}
