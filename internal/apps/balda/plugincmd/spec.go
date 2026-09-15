package plugincmd

import (
	"net/url"
	"path/filepath"
	"strings"
)

const (
	publicMarketplaceLocal    = "local"
	publicMarketplaceRedacted = "redacted"
)

const (
	CommandPlugin             = "plugin"
	CommandPluginsList        = "list"
	CommandPluginsShow        = "show"
	CommandPluginsInstall     = "install"
	CommandPluginsUpgrade     = "upgrade"
	CommandPluginsOrigin      = "origin"
	CommandPluginsEnable      = "enable"
	CommandPluginsDisable     = "disable"
	CommandPluginsRollback    = "rollback"
	CommandPluginsRemove      = "remove"
	CommandPluginsPurge       = "purge"
	CommandPluginsStatus      = "status"
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
		"- `/plugin upgrade <plugin[@marketplace]>` — activate a newer marketplace revision",
		"- `/plugin origin <plugin@marketplace>` — adopt an origin for a migrated origin-unknown install",
		"- `/plugin enable <plugin>` — enable an installed plugin",
		"- `/plugin disable <plugin>` — disable an installed plugin",
		"- `/plugin rollback <plugin> <revision>` — activate a retained revision",
		"- `/plugin remove <plugin>` — remove an installed plugin",
		"- `/plugin purge <plugin> <revision> [--data]` — purge a retired revision (and optionally data)",
		"- `/plugin status <plugin>` — show catalog and projection health",
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
		"/plugin upgrade <plugin[@marketplace]>",
		"/plugin origin <plugin@marketplace>",
		"/plugin enable <plugin>",
		"/plugin disable <plugin>",
		"/plugin rollback <plugin> <revision>",
		"/plugin remove <plugin>",
		"/plugin purge <plugin> <revision> [--data]",
		"/plugin status <plugin>",
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
		"- `/plugin upgrade <plugin[@marketplace]>`",
		"- `/plugin origin <plugin@marketplace>`",
		"- `/plugin enable <plugin>`",
		"- `/plugin disable <plugin>`",
		"- `/plugin rollback <plugin> <revision>`",
		"- `/plugin remove <plugin>`",
		"- `/plugin purge <plugin> <revision> [--data]`",
		"- `/plugin status <plugin>`",
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

// InferMarketplaceName derives the logical marketplace name from a source.
func InferMarketplaceName(source string) string {
	trimmed := strings.TrimRight(strings.TrimSuffix(strings.TrimSpace(source), ".git"), "/")
	if idx := strings.LastIndex(trimmed, "/"); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	if idx := strings.LastIndex(trimmed, ":"); idx >= 0 {
		return strings.TrimSpace(trimmed[idx+1:])
	}
	return trimmed
}

// PublicMarketplaceSource removes credentials, query values, fragments, and
// durable host paths from a marketplace source shown to users.
func PublicMarketplaceSource(source string) string {
	trimmed := strings.TrimSpace(source)
	if trimmed == "" {
		return ""
	}
	if filepath.IsAbs(trimmed) {
		return publicMarketplaceLocal
	}
	if strings.HasPrefix(trimmed, `\\`) || (len(trimmed) >= 3 && trimmed[1] == ':' && (trimmed[2] == '/' || trimmed[2] == '\\')) {
		return publicMarketplaceLocal
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return publicMarketplaceRedacted
	}
	if parsed.Scheme == "" {
		parsed.RawQuery = ""
		parsed.Fragment = ""
		return parsed.String()
	}
	if parsed.Scheme == "file" {
		return publicMarketplaceLocal
	}
	switch parsed.Scheme {
	case "git", "http", "https", "ssh":
	default:
		return publicMarketplaceRedacted
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String()
}
