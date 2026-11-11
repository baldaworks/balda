package backoffice

import (
	"sort"

	"github.com/baldaworks/balda/internal/apps/balda/systemviewcmd"
)

// ProjectSystemSnapshot captures the explicit configured fields shared by the
// embedded and standalone Backoffice views.
func ProjectSystemSnapshot(cfg ResolvedConfig) systemviewcmd.Snapshot {
	balda := cfg.Balda
	cards := make([]systemviewcmd.Capability, 0, 5)
	if balda.Telegram.Enabled {
		card := systemviewcmd.Capability{ID: "telegram", Name: "Telegram", Mode: "polling", Settings: []systemviewcmd.Setting{{Label: "Formatting", Value: configuredValue(balda.Telegram.FormattingMode)}}}
		if balda.Telegram.Webhook.Enabled {
			card.Mode = "webhook"
			card.Endpoints = appendEndpoint(card.Endpoints, "Webhook", balda.Telegram.Webhook.CallbackURL)
		}
		cards = append(cards, card)
	}
	if balda.Slack.Agent.Enabled {
		cards = append(cards, systemviewcmd.Capability{
			ID: "slackagent", Name: "Slack Agent", Mode: "agent-events",
			Settings: []systemviewcmd.Setting{
				{Label: "Private channels", Value: yesNo(balda.Slack.Agent.IncludePrivateChannels)},
				{Label: "Streaming", Value: yesNo(balda.Slack.Agent.EnableStreaming)},
				{Label: "Suggested prompts", Value: yesNo(balda.Slack.Agent.SuggestedPrompts)},
			},
			Endpoints: appendEndpoint(appendEndpoint(nil, "Events", balda.Slack.Agent.EventsCallbackURL), "Commands", balda.Slack.Agent.CommandsCallbackURL),
		})
	}
	if balda.Zulip.Webhook.Enabled {
		cards = append(cards, systemviewcmd.Capability{
			ID: "zulip", Name: "Zulip", Mode: "webhook",
			Endpoints: appendEndpoint(nil, "Webhook", balda.Zulip.Webhook.CallbackURL),
		})
	}
	if balda.Webhooks.Enabled {
		cards = append(cards, systemviewcmd.Capability{
			ID: "webhooks", Name: "Webhooks", Mode: "inbound", RouteCount: balda.Webhooks.RouteCount,
		})
	}
	if balda.Mattermost.Enabled {
		card := systemviewcmd.Capability{ID: "mattermost", Name: "Mattermost", Mode: "websocket", Settings: []systemviewcmd.Setting{{Label: "Server", Value: configuredValue(balda.Mattermost.ServerURL)}}}
		if balda.Mattermost.CommandsEnabled {
			card.Endpoints = appendEndpoint(card.Endpoints, "Commands", balda.Mattermost.CommandsCallbackURL)
		}
		cards = append(cards, card)
	}
	sort.Slice(cards, func(i, j int) bool { return cards[i].ID < cards[j].ID })
	return systemviewcmd.Snapshot{Capabilities: cards, Agent: systemviewcmd.Agent{ProviderID: cfg.Agent.ProviderID, ProviderType: cfg.Agent.ProviderType,
		Profile: cfg.Agent.Profile, Model: cfg.Agent.Model, ReasoningEffort: cfg.Agent.ReasoningEffort, GlobalInstruction: cfg.Agent.GlobalInstruction}}
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

func appendEndpoint(endpoints []systemviewcmd.Endpoint, label, value string) []systemviewcmd.Endpoint {
	if value == "" {
		return endpoints
	}
	return append(endpoints, systemviewcmd.Endpoint{Label: label, URL: value})
}
