// Package plugin owns plugin management command policy.
package plugin

import (
	"context"
	"strconv"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

const (
	transportTelegram = "telegram"
	transportZulip    = "zulip"

	suffixNotOwner           = "plugin-not-owner"
	suffixUsage              = "plugin-usage"
	suffixUnavailable        = "plugin-unavailable"
	suffixListSuccess        = "plugin-list-success"
	suffixListError          = "plugin-list-error"
	suffixShowSuccess        = "plugin-show-success"
	suffixShowError          = "plugin-show-error"
	suffixInstallSuccess     = "plugin-install-success"
	suffixInstallError       = "plugin-install-error"
	suffixRemoveSuccess      = "plugin-remove-success"
	suffixRemoveError        = "plugin-remove-error"
	suffixMarketplaceSuccess = "plugin-marketplace-success"
	suffixMarketplaceError   = "plugin-marketplace-error"

	msgNotOwner            = "Only the bot owner can use this command."
	msgServiceUnavailable  = "Plugin service is unavailable."
	msgLoadDetailsError    = "Could not load plugin details."
	msgMarketplaceNotFound = "Plugin marketplace not found."
)

// OwnerStore defines owner verification operations.
type OwnerStore interface {
	IsOwner(userID int64) bool
	IsOwnerSubject(subject string) bool
}

// Service defines plugin and marketplace operations required by the command.
type Service interface {
	ListInstalled(ctx context.Context) ([]pluginapp.PluginSummary, error)
	ListAvailable(ctx context.Context) ([]pluginapp.AvailablePlugin, error)
	GetInstalled(ctx context.Context, name string) (pluginapp.PluginSummary, bool, error)
	GetAvailable(ctx context.Context, name string) (pluginapp.AvailablePlugin, bool, error)
	Install(ctx context.Context, ref string) error
	RemoveInstalled(ctx context.Context, name string) error

	ListMarketplaceStatuses(ctx context.Context) ([]pluginapp.MarketplaceStatus, error)
	GetMarketplaceStatus(ctx context.Context, name string) (pluginapp.MarketplaceStatus, bool, error)
	AddMarketplace(ctx context.Context, src pluginapp.MarketplaceSource) error
	UpgradeMarketplaces(ctx context.Context, name string) ([]pluginapp.MarketplaceUpgradeResult, error)
	RemoveMarketplace(ctx context.Context, name string) error
}

// Handler handles the /plugin command family.
type Handler struct {
	ownerStore OwnerStore
	plugins    Service
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
}

// New creates a new plugin command Handler.
func New(
	ownerStore OwnerStore,
	plugins Service,
	dispatcher actortransport.Dispatcher,
	logger zerolog.Logger,
) *Handler {
	return &Handler{
		ownerStore: ownerStore,
		plugins:    plugins,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.plugin").Logger(),
	}
}

// Name returns "plugin".
func (h *Handler) Name() string { return plugincmd.CommandPlugin }

// Handle executes the plugin command request.
func (h *Handler) Handle(ctx context.Context, env actorlayer.Envelope, p commandcmd.Payload) error {
	if !h.isOwner(p) {
		return commandactor.SendPlain(ctx, h.dispatcher, env.ID, p.Locator, msgNotOwner, suffixNotOwner)
	}

	args := strings.TrimSpace(p.Args)
	if args == "" {
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, plugincmd.TransportUsageMarkdown(), suffixUsage)
	}

	fields := strings.Fields(args)
	if len(fields) == 0 {
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, plugincmd.TransportUsageMarkdown(), suffixUsage)
	}

	switch fields[0] {
	case plugincmd.CommandPluginsList:
		return h.sendPluginsList(ctx, env.ID, p, fields[1:])
	case plugincmd.CommandPluginsShow, plugincmd.CommandPluginsInstall, plugincmd.CommandPluginsRemove:
		return h.sendPluginsAction(ctx, env.ID, p, fields[0], fields[1:])
	case plugincmd.CommandPluginsMarketplace:
		return h.sendMarketplace(ctx, env.ID, p, fields[1:])
	default:
		return commandactor.SendMarkdown(ctx, h.dispatcher, env.ID, p.Locator, plugincmd.TransportUsageMarkdown(), suffixUsage)
	}
}

func (h *Handler) sendPluginsList(ctx context.Context, opID string, p commandcmd.Payload, rest []string) error {
	available := len(rest) == 1 && (rest[0] == "--available" || rest[0] == "available")
	if len(rest) > 1 || (len(rest) == 1 && !available) {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
	}
	if h.plugins == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgServiceUnavailable, suffixUnavailable)
	}
	if available {
		plugins, err := h.plugins.ListAvailable(ctx)
		if err != nil {
			h.logger.Error().Err(err).Msg("failed to list available plugins")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not list available plugins.", suffixListError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderAvailablePluginsMarkdown(plugins), suffixListSuccess)
	}
	plugins, err := h.plugins.ListInstalled(ctx)
	if err != nil {
		h.logger.Error().Err(err).Msg("failed to list installed plugins")
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not list installed plugins.", suffixListError)
	}
	return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderInstalledPluginsMarkdown(plugins), suffixListSuccess)
}

func (h *Handler) sendPluginsAction(ctx context.Context, opID string, p commandcmd.Payload, action string, rest []string) error {
	if len(rest) != 1 {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
	}
	if h.plugins == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgServiceUnavailable, suffixUnavailable)
	}

	switch action {
	case plugincmd.CommandPluginsShow:
		name := strings.TrimSpace(strings.SplitN(rest[0], "@", 2)[0])
		plugin, ok, err := h.plugins.GetInstalled(ctx, name)
		if err != nil {
			h.logger.Error().Err(err).Str("plugin", name).Msg("failed to get installed plugin details")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgLoadDetailsError, suffixShowError)
		}
		if ok {
			return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderInstalledPluginMarkdown(plugin), suffixShowSuccess)
		}
		available, found, err := h.plugins.GetAvailable(ctx, rest[0])
		if err != nil {
			h.logger.Error().Err(err).Str("plugin", rest[0]).Msg("failed to get available plugin details")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgLoadDetailsError, suffixShowError)
		}
		if !found {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.NotImplementedMessage("/plugin show "+rest[0]), suffixShowError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderAvailablePluginMarkdown(available), suffixShowSuccess)

	case plugincmd.CommandPluginsInstall:
		if err := h.plugins.Install(ctx, rest[0]); err != nil {
			h.logger.Error().Err(err).Str("plugin", rest[0]).Msg("failed to install plugin")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not install plugin.", suffixInstallError)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin installed.", suffixInstallSuccess)

	case plugincmd.CommandPluginsRemove:
		if err := h.plugins.RemoveInstalled(ctx, rest[0]); err != nil {
			h.logger.Error().Err(err).Str("plugin", rest[0]).Msg("failed to remove plugin")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not remove plugin.", suffixRemoveError)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin removed.", suffixRemoveSuccess)

	default:
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.NotImplementedMessage("/plugin "+action+" "+rest[0]), suffixUsage)
	}
}

func (h *Handler) sendMarketplace(ctx context.Context, opID string, p commandcmd.Payload, fields []string) error {
	if len(fields) < 1 {
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsageMarkdown(), suffixUsage)
	}

	action := fields[0]
	rest := fields[1:]

	if h.plugins == nil {
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgServiceUnavailable, suffixUnavailable)
	}

	switch action {
	case plugincmd.CommandMarketplaceList:
		if len(rest) != 0 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		sources, err := h.plugins.ListMarketplaceStatuses(ctx)
		if err != nil {
			h.logger.Error().Err(err).Msg("failed to list marketplace statuses")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not list plugin marketplaces.", suffixMarketplaceError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderMarketplaceStatusesMarkdown(sources), suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceShow:
		if len(rest) != 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		status, ok, err := h.plugins.GetMarketplaceStatus(ctx, rest[0])
		if err != nil {
			h.logger.Error().Err(err).Str("marketplace", rest[0]).Msg("failed to get marketplace status")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not load plugin marketplace.", suffixMarketplaceError)
		}
		if !ok {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgMarketplaceNotFound, suffixMarketplaceError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderMarketplaceStatusMarkdown(status), suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceAdd:
		if len(rest) < 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		source := strings.TrimSpace(rest[0])
		if source == "" {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		if err := h.plugins.AddMarketplace(ctx, pluginapp.MarketplaceSource{
			Name:   pluginapp.InferMarketplaceName(source),
			Source: source,
		}); err != nil {
			h.logger.Error().Err(err).Str("source", source).Msg("failed to add marketplace")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not add plugin marketplace.", suffixMarketplaceError)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin marketplace added.", suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceUpgrade:
		if len(rest) > 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		name := ""
		if len(rest) == 1 {
			name = strings.TrimSpace(rest[0])
		}
		results, err := h.plugins.UpgradeMarketplaces(ctx, name)
		if err != nil {
			h.logger.Error().Err(err).Str("name", name).Msg("failed to upgrade marketplaces")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not refresh plugin marketplaces.", suffixMarketplaceError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderMarketplaceUpgradeMarkdown(results), suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceRemove:
		if len(rest) != 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		if err := h.plugins.RemoveMarketplace(ctx, rest[0]); err != nil {
			h.logger.Error().Err(err).Str("name", rest[0]).Msg("failed to remove marketplace")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not remove plugin marketplace.", suffixMarketplaceError)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin marketplace removed.", suffixMarketplaceSuccess)

	default:
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsageMarkdown(), suffixUsage)
	}
}

func (h *Handler) isOwner(p commandcmd.Payload) bool {
	if p.Access.Owner {
		return true
	}
	if h.ownerStore == nil {
		return false
	}
	if p.Transport == transportTelegram {
		if id, err := strconv.ParseInt(strings.TrimSpace(p.Principal), 10, 64); err == nil {
			return h.ownerStore.IsOwner(id)
		}
	}
	subject := userSubject(p.Transport, p.Principal)
	return h.ownerStore.IsOwnerSubject(subject)
}

func userSubject(transport, principal string) string {
	trimmed := strings.TrimSpace(principal)
	switch transport {
	case transportTelegram:
		if id, err := strconv.ParseInt(trimmed, 10, 64); err == nil {
			return auth.TelegramSubject(id)
		}
	case transportZulip:
		if id, err := strconv.Atoi(trimmed); err == nil {
			return auth.ZulipSubject(id)
		}
		if id, err := strconv.Atoi(strings.TrimPrefix(trimmed, "zulip-session-")); err == nil {
			return auth.ZulipSubject(id)
		}
	}
	return transport + ":" + trimmed
}
