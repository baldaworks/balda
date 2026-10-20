package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/stateowner"
	"github.com/spf13/cobra"
)

var preparePluginCLI = prepareManagedPluginCLI

func pluginsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "plugin", Short: "Manage Balda plugins and plugin marketplaces"}
	cmd.AddCommand(pluginsListCommand(), pluginsShowCommand(), pluginsInstallCommand(), pluginsRemoveCommand(), pluginsMarketplaceCommand())
	return cmd
}

func pluginsListCommand() *cobra.Command {
	var available bool
	cmd := &cobra.Command{Use: "list", Short: "List installed plugins", RunE: func(cmd *cobra.Command, _ []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), false)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		if available {
			plugins, err := service.ListAvailable(cmd.Context())
			if err != nil {
				return publicPluginError(err)
			}
			if len(plugins) == 0 {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No available plugins.")
			}
			for _, plugin := range plugins {
				printAvailablePlugin(cmd, plugin)
			}
			return nil
		}
		plugins, err := service.InstalledInventory(cmd.Context())
		if err != nil {
			return publicPluginError(err)
		}
		if len(plugins) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No installed plugins.")
		}
		for _, plugin := range plugins {
			printInstalledPlugin(cmd, plugin)
		}
		return nil
	}}
	cmd.Flags().BoolVar(&available, "available", false, "include available marketplace plugins")
	return cmd
}

func pluginsShowCommand() *cobra.Command {
	return &cobra.Command{Use: "show <plugin[@marketplace]>", Short: "Show plugin details", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), false)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		name := strings.TrimSpace(strings.SplitN(args[0], "@", 2)[0])
		detail, err := service.InstalledDetail(cmd.Context(), name)
		if err == nil {
			printInstalledPlugin(cmd, detail.InstalledView)
			for _, skill := range detail.Skills {
				_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Skill: %s — %s\n", skill.Name, skill.Description)
			}
			return nil
		}
		if !errors.Is(err, plugincmd.ErrNotFound) {
			return publicPluginError(err)
		}
		candidate, found, err := service.GetAvailable(cmd.Context(), args[0])
		if err != nil {
			return publicPluginError(err)
		}
		if !found {
			return publicPluginError(plugincmd.ErrNotFound)
		}
		printAvailablePlugin(cmd, candidate)
		return nil
	}}
}

func pluginsInstallCommand() *cobra.Command {
	return &cobra.Command{Use: "install <plugin[@marketplace]>", Short: "Install a plugin from a configured marketplace", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), true)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		if err := service.Install(cmd.Context(), args[0]); err != nil {
			return pluginMutationError(cmd.Context(), service, args[0], err)
		}
		name := strings.TrimSpace(strings.SplitN(args[0], "@", 2)[0])
		detail, err := service.InstalledDetail(cmd.Context(), name)
		if err != nil {
			// Publication already completed; optional inspection cannot undo its outcome.
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Accepted and published: %s\n", name)
			return nil
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Accepted and published: %s revision %s\n", detail.Name, detail.Revision)
		return nil
	}}
}

func pluginsRemoveCommand() *cobra.Command {
	return &cobra.Command{Use: "remove <plugin>", Short: "Remove an installed plugin", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), true)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		if err := service.RemoveInstalled(cmd.Context(), args[0]); err != nil {
			return pluginMutationError(cmd.Context(), service, args[0], err)
		}
		_, _ = fmt.Fprintln(cmd.OutOrStdout(), "Removal accepted and published.")
		return nil
	}}
}

func pluginsMarketplaceCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "marketplace", Short: "Manage configured plugin marketplaces"}
	cmd.AddCommand(pluginsMarketplaceAddCommand(), pluginsMarketplaceListCommand(), pluginsMarketplaceShowCommand(), pluginsMarketplaceUpgradeCommand(), pluginsMarketplaceRemoveCommand())
	return cmd
}

func pluginsMarketplaceAddCommand() *cobra.Command {
	var ref string
	var sparse []string
	cmd := &cobra.Command{Use: "add <source>", Short: "Add a local or Git plugin marketplace", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), true)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		return publicPluginError(service.AddMarketplace(cmd.Context(), pluginapp.MarketplaceSource{Name: pluginapp.InferMarketplaceName(args[0]), Source: args[0], Ref: ref, Sparse: append([]string(nil), sparse...)}))
	}}
	cmd.Flags().StringVar(&ref, "ref", "", "git ref for Git marketplace sources")
	cmd.Flags().StringArrayVar(&sparse, "sparse", nil, "sparse checkout path for Git marketplace sources")
	return cmd
}

func pluginsMarketplaceListCommand() *cobra.Command {
	return &cobra.Command{Use: "list", Short: "List configured plugin marketplaces", RunE: func(cmd *cobra.Command, _ []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), false)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		statuses, err := service.ListMarketplaceStatuses(cmd.Context())
		if err != nil {
			return publicPluginError(err)
		}
		if len(statuses) == 0 {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "No configured marketplaces.")
		}
		for _, status := range statuses {
			printMarketplaceStatus(cmd, status)
		}
		return nil
	}}
}

func pluginsMarketplaceShowCommand() *cobra.Command {
	return &cobra.Command{Use: "show <name>", Short: "Show marketplace details", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), false)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		status, found, err := service.GetMarketplaceStatus(cmd.Context(), args[0])
		if err != nil {
			return publicPluginError(err)
		}
		if !found {
			return publicPluginError(plugincmd.ErrNotFound)
		}
		printMarketplaceStatus(cmd, status)
		return nil
	}}
}

func pluginsMarketplaceUpgradeCommand() *cobra.Command {
	return &cobra.Command{Use: "upgrade [name]", Short: "Refresh marketplace snapshots", Args: cobra.MaximumNArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), true)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		name := ""
		if len(args) > 0 {
			name = args[0]
		}
		results, err := service.UpgradeMarketplaces(cmd.Context(), name)
		if err != nil {
			return publicPluginError(err)
		}
		for _, result := range results {
			printMarketplaceStatus(cmd, result.Status)
		}
		return nil
	}}
}

func pluginsMarketplaceRemoveCommand() *cobra.Command {
	return &cobra.Command{Use: "remove <name>", Short: "Remove a configured marketplace source", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		service, cleanup, err := preparePluginCLI(cmd.Context(), true)
		if err != nil {
			return publicPluginError(err)
		}
		defer cleanup()
		return publicPluginError(service.RemoveMarketplace(cmd.Context(), args[0]))
	}}
}

func printInstalledPlugin(cmd *cobra.Command, view plugincmd.InstalledView) {
	activation := "Accepted and published"
	if view.RecoveryPending {
		activation = "Accepted; recovery pending"
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Name: %s\nVersion: %s\nDescription: %s\nMarketplace: %s\nRevision: %s\nEnabled: %t\nRemoved: %t\nActivation: %s\n", view.Name, view.Version, view.Description, view.Marketplace, view.Revision, view.Enabled, view.Removed, activation)
}

func printAvailablePlugin(cmd *cobra.Command, item pluginapp.AvailablePlugin) {
	view := plugincmd.AvailablePlugin{Name: item.Name, DisplayName: item.DisplayName, Description: item.Description, Version: item.Version, Marketplace: item.Marketplace, MarketplaceLabel: item.MarketplaceLabel, Category: item.Category, Installed: item.Installed}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderAvailablePluginPlain(view))
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Revision: %s\nInstallable: %t\nStale: %t\n", item.Revision, item.Installable, item.Stale)
	for _, diagnostic := range item.Diagnostics {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Diagnostic: %s\n", diagnostic.Code)
	}
}

func printMarketplaceStatus(cmd *cobra.Command, item pluginapp.MarketplaceStatus) {
	view := plugincmd.MarketplaceStatus{Name: item.Name, Source: item.Source, Kind: item.Kind, Ref: item.Ref, Sparse: append([]string(nil), item.Sparse...), Cached: item.Cached, LastRefreshedAt: item.LastRefreshedAt, ResolvedRef: item.ResolvedRef, ManifestPresent: item.ManifestPresent, AvailablePlugins: item.AvailablePlugins}
	_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderMarketplaceStatusPlain(view))
	status := item.LastAttemptStatus
	if status == "" {
		status = "not refreshed"
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Last attempt: %s\nFailure: %s\n", status, item.FailureCode)
}

func publicPluginError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, stateowner.ErrActive):
		return errors.New("runtime_active: Balda or another maintenance process owns this state directory")
	case errors.Is(err, plugincmd.ErrRecoveryPending):
		return fmt.Errorf("recovery_pending: %w", plugincmd.ErrRecoveryPending)
	case errors.Is(err, plugincmd.ErrInvalid):
		return fmt.Errorf("invalid: %w", plugincmd.ErrInvalid)
	case errors.Is(err, plugincmd.ErrConflict):
		return fmt.Errorf("conflict: %w", plugincmd.ErrConflict)
	case errors.Is(err, plugincmd.ErrNotFound):
		return fmt.Errorf("not_found: %w", plugincmd.ErrNotFound)
	default:
		return errors.New("unavailable: plugin management is unavailable")
	}
}

func pluginMutationError(ctx context.Context, service *pluginapp.Service, selector string, err error) error {
	if !errors.Is(err, plugincmd.ErrRecoveryPending) {
		return publicPluginError(err)
	}
	name := strings.TrimSpace(strings.SplitN(selector, "@", 2)[0])
	detail, readErr := service.InstalledDetail(ctx, name)
	if readErr != nil {
		return publicPluginError(err)
	}
	return fmt.Errorf("recovery_pending: Accepted; recovery pending for %s revision %s: %w", detail.Name, detail.Revision, plugincmd.ErrRecoveryPending)
}

func prepareManagedPluginCLI(ctx context.Context, mutation bool) (*pluginapp.Service, func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {}
	if mutation {
		owner, err := stateowner.Acquire(prepared.stateDir)
		if err != nil {
			return nil, nil, err
		}
		cleanup = func() { _ = owner.Close() }
	}
	provider, err := baldastate.Open(ctx, prepared.database)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	release := cleanup
	cleanup = func() { _ = provider.Close(); release() }
	catalog, err := catalogapp.NewMaintenanceRuntime(prepared.stateDir, provider, prepared.doc.Runtime.MCPServers)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if mutation {
		if err := catalog.RetainBuiltin(ctx); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	service, err := pluginapp.NewManaged(prepared.stateDir, provider.AppKV(), provider.Plugins(), catalog, provider.PluginManagement())
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return service, cleanup, nil
}
