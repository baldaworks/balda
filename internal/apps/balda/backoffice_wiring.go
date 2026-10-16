package balda

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/agentconfig"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
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

func backofficeRuntimeConfig(cfg BaldaConfig, runtimeCfg runtimeconfig.RuntimeConfig, profile string, database state.DatabaseConfig) (backoffice.ResolvedConfig, error) {
	sharedHTTP, err := cfg.ResolveHTTP()
	if err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	server, err := cfg.ResolveSharedBackofficeServer()
	if err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	agent, err := projectBackofficeAgent(cfg, runtimeCfg, profile)
	if err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	callback := func(enabled bool, transport, endpoint string) string {
		if !enabled {
			return ""
		}
		callbackPath := sharedHTTP.GatewayPath(transport, endpoint)
		if strings.TrimSpace(cfg.HTTP.BaseURL) == "" {
			return callbackPath
		}
		return sharedHTTP.PublicURL(callbackPath)
	}
	projected := backoffice.BaldaConfig{
		Telegram: backoffice.TelegramConfig{Enabled: strings.TrimSpace(cfg.Telegram.Token) != "", FormattingMode: cfg.Telegram.FormattingMode, Webhook: backoffice.TelegramWebhookConfig{
			Enabled:     cfg.Telegram.Webhook.Enabled,
			CallbackURL: callback(cfg.Telegram.Webhook.Enabled, "telegram", "webhook"),
		}},
		Zulip: backoffice.ZulipConfig{Webhook: backoffice.ZulipWebhookConfig{
			Enabled:     cfg.Zulip.Webhook.Enabled,
			CallbackURL: callback(cfg.Zulip.Webhook.Enabled, "zulip", "webhook"),
		}},
		Slack: backoffice.SlackConfig{
			Agent: backoffice.SlackAgentConfig{
				Enabled: cfg.Slack.Agent.Enabled, IncludePrivateChannels: cfg.Slack.IncludePrivateChannels,
				EnableStreaming: cfg.Slack.Agent.EnableStreaming, SuggestedPrompts: cfg.Slack.Agent.SuggestedPrompts,
				EventsCallbackURL:   callback(cfg.Slack.Agent.Enabled, "slack", "events"),
				CommandsCallbackURL: callback(cfg.Slack.Agent.Enabled, "slack", "commands"),
			},
		},
		Webhooks: backoffice.WebhooksConfig{
			Enabled: cfg.Webhooks.Enabled, RouteCount: len(cfg.Webhooks.Routes),
		},
		Mattermost: backoffice.MattermostConfig{
			Enabled: cfg.Mattermost.Enabled, ServerURL: sanitizeBackofficeURL(cfg.Mattermost.ServerURL),
			CommandsEnabled:     cfg.Mattermost.CommandsEnabled,
			CommandsCallbackURL: callback(cfg.Mattermost.CommandsEnabled, "mattermost", "commands"),
		},
	}
	return backoffice.ResolvedConfig{Agent: agent, Balda: projected, Server: server, Database: database}, nil
}

func projectBackofficeAgent(cfg BaldaConfig, runtimeCfg runtimeconfig.RuntimeConfig, profile string) (backoffice.AgentConfig, error) {
	providerID := strings.TrimSpace(cfg.Provider)
	projected := backoffice.AgentConfig{ProviderID: providerID, Profile: strings.TrimSpace(profile), GlobalInstruction: cfg.GlobalInstruction}
	if providerID == "" {
		return projected, nil
	}
	provider, ok := runtimeCfg.Providers[providerID]
	if !ok {
		return backoffice.AgentConfig{}, fmt.Errorf("backoffice agent provider %q is not configured", providerID)
	}
	projected.ProviderType = strings.TrimSpace(provider.Type)
	if projected.ProviderType == agentconfig.AgentTypePool {
		return projected, nil
	}
	resolved, err := agentconfig.NormalizeConfig(provider, "")
	if err != nil {
		return backoffice.AgentConfig{}, fmt.Errorf("normalize backoffice agent provider %q: %w", providerID, err)
	}
	projected.Model = strings.TrimSpace(resolved.Model)
	projected.ReasoningEffort = strings.TrimSpace(resolved.ReasoningEffort)
	return projected, nil
}

func sanitizeBackofficeURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	parsed.RawFragment = ""
	return parsed.String()
}
