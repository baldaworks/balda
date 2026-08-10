package plugincmd

import "strings"

const (
	CommandPlugin             = "plugin"
	CommandPluginsList        = "list"
	CommandPluginsShow        = "show"
	CommandPluginsInstall     = "install"
	CommandPluginsRemove      = "remove"
	CommandPluginsMarketplace = "marketplace"
	CommandMarketplaceAdd     = "add"
	CommandMarketplaceList    = "list"
	CommandMarketplaceShow    = "show"
	CommandMarketplaceUpgrade = "upgrade"
	CommandMarketplaceRemove  = "remove"
)

func HelpMarkdown() string {
	lines := []string{
		"## Plugin",
		"",
		"- `/plugin list` — list installed plugins",
		"- `/plugin list --available` — list available marketplace plugins",
		"- `/plugin show <plugin[@marketplace]>` — show plugin details",
		"- `/plugin install <plugin[@marketplace]>` — install a plugin",
		"- `/plugin remove <plugin>` — remove an installed plugin",
		"",
		"## Plugin marketplaces",
		"",
		"- `/plugin marketplace add <source>` — add a marketplace source",
		"- `/plugin marketplace list` — list configured marketplaces",
		"- `/plugin marketplace show <name>` — show marketplace details",
		"- `/plugin marketplace upgrade [name]` — refresh marketplace snapshots",
		"- `/plugin marketplace remove <name>` — remove a marketplace source",
	}
	return strings.Join(lines, "\n")
}

func TransportUsage() string {
	return strings.Join([]string{
		"Usage:",
		"/plugin list",
		"/plugin list --available",
		"/plugin show <plugin[@marketplace]>",
		"/plugin install <plugin[@marketplace]>",
		"/plugin remove <plugin>",
		"/plugin marketplace add <source>",
		"/plugin marketplace list",
		"/plugin marketplace show <name>",
		"/plugin marketplace upgrade [name]",
		"/plugin marketplace remove <name>",
	}, "\n")
}

func TransportUsageMarkdown() string {
	lines := []string{
		"# Plugin commands",
		"",
		"## Installed plugins",
		"",
		"- `/plugin list`",
		"- `/plugin list --available`",
		"- `/plugin show <plugin[@marketplace]>`",
		"- `/plugin install <plugin[@marketplace]>`",
		"- `/plugin remove <plugin>`",
		"",
		"## Marketplaces",
		"",
		"- `/plugin marketplace add <source>`",
		"- `/plugin marketplace list`",
		"- `/plugin marketplace show <name>`",
		"- `/plugin marketplace upgrade [name]`",
		"- `/plugin marketplace remove <name>`",
	}

	return strings.Join(lines, "\n")
}

func NotImplementedMessage(selector string) string {
	if strings.TrimSpace(selector) == "" {
		return "Plugin command shape is wired, but the plugin marketplace backend is not implemented yet."
	}
	return "Plugin command `" + strings.TrimSpace(selector) + "` is wired, but the plugin marketplace backend is not implemented yet."
}
