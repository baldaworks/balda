package balda

import (
	"context"
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

func backofficeSharedHandler(ctx context.Context, runtime *backoffice.Runtime, cfg BaldaConfig, mcp *mcpbackofficeapp.Operations) (http.Handler, error) {
	server, err := cfg.ResolveSharedBackofficeServer()
	if err != nil {
		return nil, err
	}
	services := backoffice.HandlerServices{}
	if mcp != nil {
		services.MCP, services.MCPAuthorizations = mcp, mcp
	}
	return runtime.Handler(ctx, server, services)
}

func newSharedBackofficeMCP(cfg BaldaConfig, grants *mcpmanage.Grants, definitions *mcpmanage.Definitions, catalog *catalogapp.Runtime) (*mcpbackofficeapp.Operations, *mcpmanage.Authorizations, error) {
	server, err := cfg.ResolveSharedBackofficeServer()
	if err != nil {
		return nil, nil, err
	}
	callback, err := mcpfx.MCPCallbackURL(server.PublicURL, server.BasePath)
	if err != nil {
		return nil, nil, err
	}
	authorizations, err := mcpmanage.NewAuthorizations(grants, callback, definitions)
	if err != nil {
		return nil, nil, err
	}
	operations := mcpbackofficeapp.New(definitions, catalog)
	if err := operations.ConfigureAuthorizations(authorizations); err != nil {
		authorizations.Close()
		return nil, nil, err
	}
	return operations, authorizations, nil
}

func backofficeRuntimeConfig(cfg BaldaConfig, database state.DatabaseConfig) (backoffice.ResolvedConfig, error) {
	if _, err := cfg.ResolveHTTP(); err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	server, err := cfg.ResolveSharedBackofficeServer()
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
		Mattermost: backoffice.MattermostConfig{
			Enabled: cfg.Mattermost.Enabled, ServerURL: cfg.Mattermost.ServerURL,
			CommandsEnabled: cfg.Mattermost.CommandsEnabled, ListenAddr: cfg.Mattermost.CommandsListenAddr,
			CommandsPath: cfg.Mattermost.CommandsPath,
		},
	}
	return backoffice.ResolvedConfig{Balda: projected, Server: server, Database: database}, nil
}
