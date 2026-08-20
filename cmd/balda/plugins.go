package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/paths"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/spf13/cobra"
)

func pluginsCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "plugin",
		Short: "Manage Balda plugins and plugin marketplaces",
	}
	cmd.AddCommand(pluginsListCommand())
	cmd.AddCommand(pluginsShowCommand())
	cmd.AddCommand(pluginsInstallCommand())
	cmd.AddCommand(pluginsRemoveCommand())
	cmd.AddCommand(pluginsMarketplaceCommand())
	return cmd
}

func pluginsListCommand() *cobra.Command {
	var available bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List installed plugins",
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, err := preparePluginService(cmd.Context())
			if err != nil {
				return err
			}
			if available {
				plugins, err := service.ListAvailable(context.Background())
				if err != nil {
					return err
				}
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderAvailablePluginsPlain(plugins))
				return nil
			}
			plugins, err := service.ListInstalled(context.Background())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderInstalledPluginsPlain(plugins))
			return nil
		},
	}
	cmd.Flags().BoolVar(&available, "available", false, "include available marketplace plugins")
	return cmd
}

func pluginsShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <plugin[@marketplace]>",
		Short: "Show plugin details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, err := preparePluginService(cmd.Context())
			if err != nil {
				return err
			}
			name := strings.TrimSpace(strings.SplitN(args[0], "@", 2)[0])
			plugin, ok, err := service.GetInstalled(context.Background(), name)
			if err != nil {
				return err
			}
			if ok {
				_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderInstalledPluginPlain(plugin))
				return nil
			}
			available, found, err := service.GetAvailable(context.Background(), args[0])
			if err != nil {
				return err
			}
			if !found {
				return errors.New(plugincmd.NotImplementedMessage("plugin show " + args[0]))
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderAvailablePluginPlain(available))
			return nil
		},
	}
}

func pluginsInstallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "install <plugin[@marketplace]>",
		Short: "Install a plugin from a configured marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			return service.Install(context.Background(), args[0])
		},
	}
}

func pluginsRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <plugin>",
		Short: "Remove an installed plugin",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			return service.RemoveInstalled(context.Background(), args[0])
		},
	}
}

func pluginsMarketplaceCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "marketplace",
		Short: "Manage configured plugin marketplaces",
	}
	cmd.AddCommand(pluginsMarketplaceAddCommand())
	cmd.AddCommand(pluginsMarketplaceListCommand())
	cmd.AddCommand(pluginsMarketplaceShowCommand())
	cmd.AddCommand(pluginsMarketplaceUpgradeCommand())
	cmd.AddCommand(pluginsMarketplaceRemoveCommand())
	return cmd
}

func pluginsMarketplaceAddCommand() *cobra.Command {
	var ref string
	var sparse []string
	cmd := &cobra.Command{
		Use:   "add <source>",
		Short: "Add a local or Git plugin marketplace",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			_ = ref
			_ = sparse
			service, cleanup, err := preparePluginServiceWithCleanup(context.Background())
			if err != nil {
				return err
			}
			defer cleanup()
			src := pluginapp.MarketplaceSource{
				Name:   pluginapp.InferMarketplaceName(args[0]),
				Source: args[0],
				Ref:    ref,
				Sparse: append([]string(nil), sparse...),
			}
			if err := service.AddMarketplace(context.Background(), src); err != nil {
				return err
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "git ref for Git marketplace sources")
	cmd.Flags().StringArrayVar(&sparse, "sparse", nil, "sparse checkout path for Git marketplace sources")
	return cmd
}

func pluginsMarketplaceListCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List configured plugin marketplaces",
		RunE: func(cmd *cobra.Command, _ []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(context.Background())
			if err != nil {
				return err
			}
			defer cleanup()
			sources, err := service.ListMarketplaceStatuses(context.Background())
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderMarketplaceStatusesPlain(sources))
			return nil
		},
	}
}

func pluginsMarketplaceUpgradeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade [name]",
		Short: "Refresh marketplace snapshots",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			name := ""
			if len(args) == 1 {
				name = args[0]
			}
			results, err := service.UpgradeMarketplaces(context.Background(), name)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderMarketplaceUpgradePlain(results))
			return nil
		},
	}
}

func pluginsMarketplaceShowCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "show <name>",
		Short: "Show marketplace details",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(cmd.Context())
			if err != nil {
				return err
			}
			defer cleanup()
			status, ok, err := service.GetMarketplaceStatus(context.Background(), args[0])
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("plugin marketplace not found")
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), plugincmd.RenderMarketplaceStatusPlain(status))
			return nil
		},
	}
}

func pluginsMarketplaceRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "remove <name>",
		Short: "Remove a configured marketplace source",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			service, cleanup, err := preparePluginServiceWithCleanup(context.Background())
			if err != nil {
				return err
			}
			defer cleanup()
			return service.RemoveMarketplace(context.Background(), args[0])
		},
	}
}

func preparePluginService(ctx context.Context) (*pluginapp.Service, error) {
	service, cleanup, err := preparePluginServiceWithCleanup(ctx)
	if err != nil {
		return nil, err
	}
	_ = cleanup
	return service, nil
}

func preparePluginServiceWithCleanup(ctx context.Context) (*pluginapp.Service, func(), error) {
	prepared, err := prepareBaldaCommand(ctx)
	if err != nil {
		return nil, nil, err
	}
	provider, err := baldastate.NewSQLiteProvider(ctx, paths.StateDBPath(prepared.stateDir))
	if err != nil {
		return nil, nil, err
	}
	service, err := pluginapp.New(prepared.stateDir, provider.AppKV())
	if err != nil {
		_ = provider.Close()
		return nil, nil, err
	}
	return service, func() { _ = provider.Close() }, nil
}
