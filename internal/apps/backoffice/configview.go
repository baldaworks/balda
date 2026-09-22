package backoffice

import (
	"sort"
	"strings"
)

// ConfiguredCapability is a secret-free read-only projection of one enabled integration.
type ConfiguredCapability struct {
	ID         string
	Name       string
	Mode       string
	ListenAddr string
	Endpoint   string
	RouteCount int
	Streaming  bool
}

// ProjectConfiguredCapabilities returns only integrations enabled by resolved configuration.
func ProjectConfiguredCapabilities(cfg BaldaConfig) []ConfiguredCapability {
	capabilities := make([]ConfiguredCapability, 0, 5)
	if strings.TrimSpace(cfg.Telegram.Token) != "" {
		capability := ConfiguredCapability{ID: "telegram", Name: "Telegram", Mode: "polling"}
		if cfg.Telegram.Webhook.Enabled {
			capability.Mode = "webhook"
			capability.ListenAddr = strings.TrimSpace(cfg.Telegram.Webhook.ListenAddr)
			capability.Endpoint = strings.TrimSpace(cfg.Telegram.Webhook.Path)
		}
		capabilities = append(capabilities, capability)
	}
	if cfg.Slack.Enabled {
		capabilities = append(capabilities, ConfiguredCapability{
			ID: "slack-chat", Name: "Slack chat", Mode: "events",
			ListenAddr: strings.TrimSpace(cfg.Slack.ListenAddr), Endpoint: strings.TrimSpace(cfg.Slack.EventsPath),
		})
	}
	if cfg.Slack.Agent.Enabled {
		capabilities = append(capabilities, ConfiguredCapability{
			ID: "slack-agent", Name: "Slack Agent", Mode: "agent-events",
			ListenAddr: strings.TrimSpace(cfg.Slack.Agent.ListenAddr), Endpoint: strings.TrimSpace(cfg.Slack.Agent.EventsPath),
			Streaming: cfg.Slack.Agent.EnableStreaming,
		})
	}
	if cfg.Zulip.Webhook.Enabled {
		capabilities = append(capabilities, ConfiguredCapability{
			ID: "zulip", Name: "Zulip", Mode: "webhook",
			ListenAddr: strings.TrimSpace(cfg.Zulip.Webhook.ListenAddr), Endpoint: strings.TrimSpace(cfg.Zulip.Webhook.Path),
		})
	}
	if cfg.Webhooks.Enabled {
		capabilities = append(capabilities, ConfiguredCapability{
			ID: "webhooks", Name: "Webhooks", Mode: "inbound",
			ListenAddr: strings.TrimSpace(cfg.Webhooks.ListenAddr), RouteCount: len(cfg.Webhooks.Routes),
		})
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].ID < capabilities[j].ID })
	return capabilities
}
