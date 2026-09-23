package balda

import (
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func backofficeRuntimeConfig(cfg BaldaConfig, database state.DatabaseConfig) (backoffice.ResolvedConfig, error) {
	server, err := cfg.Backoffice.Resolve()
	if err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	projected := backoffice.BaldaConfig{
		Telegram: backoffice.TelegramConfig{Enabled: strings.TrimSpace(cfg.Telegram.Token) != "", Webhook: backoffice.TelegramWebhookConfig{
			Enabled: cfg.Telegram.Webhook.Enabled, ListenAddr: cfg.Telegram.Webhook.ListenAddr, Path: cfg.Telegram.Webhook.Path,
		}},
		Zulip: backoffice.ZulipConfig{Webhook: backoffice.ZulipWebhookConfig{
			Enabled: cfg.Zulip.Webhook.Enabled, ListenAddr: cfg.Zulip.Webhook.ListenAddr, Path: cfg.Zulip.Webhook.Path,
		}},
		Slack: backoffice.SlackConfig{
			Enabled: cfg.Slack.Enabled, ListenAddr: cfg.Slack.ListenAddr, EventsPath: cfg.Slack.EventsPath,
			Agent: backoffice.SlackAgentConfig{
				Enabled: cfg.Slack.Agent.Enabled, ListenAddr: cfg.Slack.Agent.ListenAddr,
				EventsPath: cfg.Slack.Agent.EventsPath, EnableStreaming: cfg.Slack.Agent.EnableStreaming,
			},
		},
		Webhooks: backoffice.WebhooksConfig{
			Enabled: cfg.Webhooks.Enabled, ListenAddr: cfg.Webhooks.ListenAddr,
			RouteCount: len(cfg.Webhooks.Routes),
		},
	}
	return backoffice.ResolvedConfig{Balda: projected, Server: server, Database: database}, nil
}
