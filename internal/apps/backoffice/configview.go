package backoffice

import (
	"sort"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
)

// ProjectCapabilityCards converts the safe configured projection into Web UI cards.
func ProjectCapabilityCards(cfg BaldaConfig) []webui.CapabilityCard {
	cards := make([]webui.CapabilityCard, 0, 5)
	if cfg.Telegram.Enabled {
		card := webui.CapabilityCard{ID: "telegram", Name: "Telegram", Mode: "polling", Settings: []webui.CapabilitySetting{{Label: "Formatting", Value: configuredValue(cfg.Telegram.FormattingMode)}}}
		if cfg.Telegram.Webhook.Enabled {
			card.Mode = "webhook"
			card.Endpoints = appendEndpoint(card.Endpoints, "Webhook", cfg.Telegram.Webhook.CallbackURL)
		}
		cards = append(cards, card)
	}
	if cfg.Slack.Agent.Enabled {
		cards = append(cards, webui.CapabilityCard{
			ID: "slackagent", Name: "Slack Agent", Mode: "agent-events",
			Settings: []webui.CapabilitySetting{
				{Label: "Private channels", Value: yesNo(cfg.Slack.Agent.IncludePrivateChannels)},
				{Label: "Streaming", Value: yesNo(cfg.Slack.Agent.EnableStreaming)},
				{Label: "Suggested prompts", Value: yesNo(cfg.Slack.Agent.SuggestedPrompts)},
			},
			Endpoints: appendEndpoint(appendEndpoint(nil, "Events", cfg.Slack.Agent.EventsCallbackURL), "Commands", cfg.Slack.Agent.CommandsCallbackURL),
		})
	}
	if cfg.Zulip.Webhook.Enabled {
		cards = append(cards, webui.CapabilityCard{
			ID: "zulip", Name: "Zulip", Mode: "webhook",
			Endpoints: appendEndpoint(nil, "Webhook", cfg.Zulip.Webhook.CallbackURL),
		})
	}
	if cfg.Webhooks.Enabled {
		cards = append(cards, webui.CapabilityCard{
			ID: "webhooks", Name: "Webhooks", Mode: "inbound", RouteCount: cfg.Webhooks.RouteCount,
		})
	}
	if cfg.Mattermost.Enabled {
		card := webui.CapabilityCard{ID: "mattermost", Name: "Mattermost", Mode: "websocket", Settings: []webui.CapabilitySetting{{Label: "Server", Value: configuredValue(cfg.Mattermost.ServerURL)}}}
		if cfg.Mattermost.CommandsEnabled {
			card.Endpoints = appendEndpoint(card.Endpoints, "Commands", cfg.Mattermost.CommandsCallbackURL)
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return cards
}

func yesNo(value bool) string {
	if value {
		return "Yes"
	}
	return "No"
}

func configuredValue(value string) string {
	if value == "" {
		return "Not configured"
	}
	return value
}

func appendEndpoint(endpoints []webui.CapabilityEndpoint, label, value string) []webui.CapabilityEndpoint {
	if value == "" {
		return endpoints
	}
	return append(endpoints, webui.CapabilityEndpoint{Label: label, URL: value})
}

func configuredBindingChannels(cfg BaldaConfig) []string {
	var channels []string
	if cfg.Telegram.Enabled {
		channels = append(channels, "telegram")
	}
	if cfg.Slack.Agent.Enabled {
		channels = append(channels, "slackagent")
	}
	if cfg.Zulip.Webhook.Enabled {
		channels = append(channels, "zulip")
	}
	if cfg.Mattermost.Enabled {
		channels = append(channels, "mattermost")
	}
	return channels
}
