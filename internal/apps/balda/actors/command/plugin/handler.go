// Package plugin owns plugin management command policy.
package plugin

import (
	"context"
	"strconv"
	"strings"

	commandactor "github.com/baldaworks/balda/internal/apps/balda/actors/command"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
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
	suffixRemoveSuccess      = "plugin-remove-success"
	suffixRemoveError        = "plugin-remove-error"
	suffixLifecycleSuccess   = "plugin-lifecycle-success"
	suffixLifecycleError     = "plugin-lifecycle-error"
	suffixStatusSuccess      = "plugin-status-success"
	suffixStatusError        = "plugin-status-error"
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
	ListInstalled(ctx context.Context) ([]plugincmd.PluginSummary, error)
	ListAvailable(ctx context.Context) ([]plugincmd.AvailablePlugin, error)
	GetInstalled(ctx context.Context, name string) (plugincmd.PluginSummary, bool, error)
	GetAvailable(ctx context.Context, name string) (plugincmd.AvailablePlugin, bool, error)
	Install(ctx context.Context, ref string) error
	Upgrade(ctx context.Context, ref string) error
	Enable(ctx context.Context, name string) error
	Disable(ctx context.Context, name string) error
	Rollback(ctx context.Context, name, revision string) error
	RemoveInstalled(ctx context.Context, name string) error
	Purge(ctx context.Context, name, revision string, purgeData bool) error
	Status(ctx context.Context, name string) (plugincmd.PluginStatus, bool, error)

	ListMarketplaceStatuses(ctx context.Context) ([]plugincmd.MarketplaceStatus, error)
	GetMarketplaceStatus(ctx context.Context, name string) (plugincmd.MarketplaceStatus, bool, error)
	AddMarketplace(ctx context.Context, src plugincmd.MarketplaceSource) error
	UpgradeMarketplaces(ctx context.Context, name string) ([]plugincmd.MarketplaceUpgradeResult, error)
	RemoveMarketplace(ctx context.Context, name string) error
}

// OperationEvent is a bounded audit/metric event for owner management actions.
type OperationEvent struct {
	Operation string
	Outcome   string
}

// Observer receives redacted operation counters and audit events.
type Observer interface {
	ObservePluginOperation(ctx context.Context, event OperationEvent)
}

// Handler handles the /plugin command family.
type Handler struct {
	ownerStore OwnerStore
	plugins    Service
	dispatcher actortransport.Dispatcher
	logger     zerolog.Logger
	observer   Observer
}

// New creates a new plugin command Handler.
func New(
	ownerStore OwnerStore,
	plugins Service,
	dispatcher actortransport.Dispatcher,
	logger zerolog.Logger,
	observers ...Observer,
) *Handler {
	var observer Observer
	if len(observers) > 0 {
		observer = observers[0]
	}
	return &Handler{
		ownerStore: ownerStore,
		plugins:    plugins,
		dispatcher: dispatcher,
		logger:     logger.With().Str("component", "balda.actors.command.plugin").Logger(),
		observer:   observer,
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
	case plugincmd.CommandPluginsShow, plugincmd.CommandPluginsInstall, plugincmd.CommandPluginsUpgrade,
		plugincmd.CommandPluginsEnable, plugincmd.CommandPluginsDisable, plugincmd.CommandPluginsRemove,
		plugincmd.CommandPluginsStatus:
		return h.sendPluginsAction(ctx, env.ID, p, fields[0], fields[1:])
	case plugincmd.CommandPluginsRollback:
		return h.sendRollback(ctx, env.ID, p, fields[1:])
	case plugincmd.CommandPluginsPurge:
		return h.sendPurge(ctx, env.ID, p, fields[1:])
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
			h.logger.Error().Str("error_class", "list_available_failed").Msg("failed to list available plugins")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not list available plugins.", suffixListError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderAvailablePluginsMarkdown(plugins), suffixListSuccess)
	}
	plugins, err := h.plugins.ListInstalled(ctx)
	if err != nil {
		h.logger.Error().Str("error_class", "list_installed_failed").Msg("failed to list installed plugins")
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
			h.logger.Error().Str("error_class", "show_installed_failed").Msg("failed to get installed plugin details")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgLoadDetailsError, suffixShowError)
		}
		if ok {
			return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderInstalledPluginMarkdown(plugin), suffixShowSuccess)
		}
		available, found, err := h.plugins.GetAvailable(ctx, rest[0])
		if err != nil {
			h.logger.Error().Str("error_class", "show_available_failed").Msg("failed to get available plugin details")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgLoadDetailsError, suffixShowError)
		}
		if !found {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.NotImplementedMessage("/plugin show "+rest[0]), suffixShowError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderAvailablePluginMarkdown(available), suffixShowSuccess)

	case plugincmd.CommandPluginsInstall:
		return h.runLifecycle(ctx, opID, p, action, rest[0], "Plugin installed.", "Could not install plugin.", h.plugins.Install)

	case plugincmd.CommandPluginsUpgrade:
		return h.runLifecycle(ctx, opID, p, action, rest[0], "Plugin upgraded.", "Could not upgrade plugin.", h.plugins.Upgrade)

	case plugincmd.CommandPluginsEnable:
		return h.runLifecycle(ctx, opID, p, action, rest[0], "Plugin enabled.", "Could not enable plugin.", h.plugins.Enable)

	case plugincmd.CommandPluginsDisable:
		return h.runLifecycle(ctx, opID, p, action, rest[0], "Plugin disabled.", "Could not disable plugin.", h.plugins.Disable)

	case plugincmd.CommandPluginsStatus:
		status, found, err := h.plugins.Status(ctx, rest[0])
		if err != nil {
			h.observe(ctx, action, "error")
			h.logger.Error().Str("error_class", "status_failed").Msg("failed to inspect plugin status")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not load plugin status.", suffixStatusError)
		}
		if !found {
			h.observe(ctx, action, "not_found")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin not installed.", suffixStatusError)
		}
		h.observe(ctx, action, "success")
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderPluginStatusMarkdown(status), suffixStatusSuccess)

	case plugincmd.CommandPluginsRemove:
		if err := h.plugins.RemoveInstalled(ctx, rest[0]); err != nil {
			h.observe(ctx, action, "error")
			h.logger.Error().Str("error_class", "remove_failed").Msg("failed to remove plugin")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not remove plugin.", suffixRemoveError)
		}
		h.observe(ctx, action, "success")
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin removed.", suffixRemoveSuccess)

	default:
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.NotImplementedMessage("/plugin "+action+" "+rest[0]), suffixUsage)
	}
}

func (h *Handler) runLifecycle(
	ctx context.Context,
	opID string,
	p commandcmd.Payload,
	operation string,
	target string,
	successMessage string,
	errorMessage string,
	run func(context.Context, string) error,
) error {
	if err := run(ctx, target); err != nil {
		h.observe(ctx, operation, "error")
		h.logger.Error().Str("error_class", "operation_failed").Str("operation", operation).Msg("plugin lifecycle operation failed")
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, errorMessage, suffixLifecycleError)
	}
	h.observe(ctx, operation, "success")
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, successMessage, suffixLifecycleSuccess)
}

func (h *Handler) sendRollback(ctx context.Context, opID string, p commandcmd.Payload, rest []string) error {
	if len(rest) != 2 || h.plugins == nil {
		if h.plugins == nil {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgServiceUnavailable, suffixUnavailable)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
	}
	if err := h.plugins.Rollback(ctx, rest[0], rest[1]); err != nil {
		h.observe(ctx, plugincmd.CommandPluginsRollback, "error")
		h.logger.Error().Str("error_class", "rollback_failed").Msg("plugin rollback failed")
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not roll back plugin.", suffixLifecycleError)
	}
	h.observe(ctx, plugincmd.CommandPluginsRollback, "success")
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin rolled back.", suffixLifecycleSuccess)
}

func (h *Handler) sendPurge(ctx context.Context, opID string, p commandcmd.Payload, rest []string) error {
	valid := len(rest) == 2 || (len(rest) == 3 && rest[2] == "--data")
	if !valid || h.plugins == nil {
		if h.plugins == nil {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, msgServiceUnavailable, suffixUnavailable)
		}
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
	}
	if err := h.plugins.Purge(ctx, rest[0], rest[1], len(rest) == 3); err != nil {
		h.observe(ctx, plugincmd.CommandPluginsPurge, "error")
		h.logger.Error().Str("error_class", "purge_failed").Msg("plugin purge failed")
		return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not purge plugin revision.", suffixLifecycleError)
	}
	h.observe(ctx, plugincmd.CommandPluginsPurge, "success")
	return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Plugin revision purged.", suffixLifecycleSuccess)
}

func (h *Handler) observe(ctx context.Context, operation, outcome string) {
	h.logger.Info().Str("audit_event", "plugin_management").Str("operation", operation).Str("outcome", outcome).Msg("plugin management operation")
	if h.observer != nil {
		h.observer.ObservePluginOperation(ctx, OperationEvent{Operation: operation, Outcome: outcome})
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
			h.logger.Error().Str("error_class", "marketplace_list_failed").Msg("failed to list marketplace statuses")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not list plugin marketplaces.", suffixMarketplaceError)
		}
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderMarketplaceStatusesMarkdown(sources), suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceShow:
		if len(rest) != 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		status, ok, err := h.plugins.GetMarketplaceStatus(ctx, rest[0])
		if err != nil {
			h.logger.Error().Str("error_class", "marketplace_show_failed").Msg("failed to get marketplace status")
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
		if err := h.plugins.AddMarketplace(ctx, plugincmd.MarketplaceSource{
			Name:   plugincmd.InferMarketplaceName(source),
			Source: source,
		}); err != nil {
			h.observe(ctx, "marketplace_add", "error")
			h.logger.Error().Str("error_class", "marketplace_add_failed").Msg("failed to add marketplace")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not add plugin marketplace.", suffixMarketplaceError)
		}
		h.observe(ctx, "marketplace_add", "success")
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
			h.observe(ctx, "marketplace_upgrade", "error")
			h.logger.Error().Str("error_class", "marketplace_upgrade_failed").Msg("failed to upgrade marketplaces")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not refresh plugin marketplaces.", suffixMarketplaceError)
		}
		h.observe(ctx, "marketplace_upgrade", "success")
		return commandactor.SendMarkdown(ctx, h.dispatcher, opID, p.Locator, plugincmd.RenderMarketplaceUpgradeMarkdown(results), suffixMarketplaceSuccess)

	case plugincmd.CommandMarketplaceRemove:
		if len(rest) != 1 {
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, plugincmd.TransportUsage(), suffixUsage)
		}
		if err := h.plugins.RemoveMarketplace(ctx, rest[0]); err != nil {
			h.observe(ctx, "marketplace_remove", "error")
			h.logger.Error().Str("error_class", "marketplace_remove_failed").Msg("failed to remove marketplace")
			return commandactor.SendPlain(ctx, h.dispatcher, opID, p.Locator, "Could not remove plugin marketplace.", suffixMarketplaceError)
		}
		h.observe(ctx, "marketplace_remove", "success")
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
