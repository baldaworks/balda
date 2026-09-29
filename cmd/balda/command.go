package main

import (
	"context"
	_ "embed"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda"
	"github.com/baldaworks/balda/internal/apps/balda/paths"
	"github.com/baldaworks/balda/internal/apps/balda/shutdown"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/appconfig"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.uber.org/fx"
)

//go:embed balda.yaml
var defaultBaldaConfig []byte

const shutdownTimeout = 10 * time.Second

type baldaConfigDocument struct {
	Runtime appconfig.RuntimeConfig `mapstructure:"runtime"`
	Balda   balda.BaldaConfig       `mapstructure:"balda"`
}

type preparedBaldaCommand struct {
	workingDir      string
	stateDir        string
	doc             baldaConfigDocument
	baldaCfg        balda.Config
	runtimeLoadOpts appconfig.RuntimeLoadOptions
	ownerToken      string
	database        state.DatabaseConfig
}

var (
	validateBaldaApplicationFn = validateBaldaApplication
	preflightBaldaRuntimeFn    = preflightBaldaRuntime
)

func startCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "start",
		Short:         "Start Balda, Backoffice, and enabled integrations",
		Long:          "Apply embedded schema migrations, check user readiness, then start Balda, Backoffice, and enabled integrations.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prepared, err := prepareBaldaCommand(cmd.Context(), true)
			if err != nil {
				return err
			}

			app := balda.App(
				prepared.baldaCfg,
				prepared.doc.Runtime,
				prepared.ownerToken,
				prepared.runtimeLoadOpts,
				defaultBaldaConfig,
			)

			ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()

			if err := app.Start(ctx); err != nil {
				return fmt.Errorf("starting Balda app: %w", err)
			}

			logBaldaStartup(ctx, prepared.baldaCfg.Balda.Telegram.Token)

			var runtimeErr error
			select {
			case <-ctx.Done():
			case signal := <-app.Wait():
				if signal.ExitCode != 0 {
					runtimeErr = fmt.Errorf("balda runtime requested shutdown with exit code %d", signal.ExitCode)
				}
			}
			shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
			defer shutdownCancel()
			if err := app.Stop(shutdownCtx); err != nil {
				if shutdown.IsExpected(err) {
					return runtimeErr
				}
				return fmt.Errorf("stopping Balda app: %w", err)
			}

			return runtimeErr
		},
	}

	return cmd
}

func validateCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "validate",
		Short:         "Validate Balda configuration",
		Long:          "Load and validate Balda configuration without starting the provider runtime.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prepared, err := loadBaldaCommandConfig(true)
			if err != nil {
				return err
			}
			if err := validateBaldaApplicationFn(prepared); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "balda validate: ok")
			return nil
		},
	}
	return cmd
}

func preflightCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "preflight",
		Short:         "Validate Balda configuration and provider runtime readiness",
		Long:          "Load Balda configuration, validate app wiring, start the configured provider runtime, and stop it cleanly.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prepared, err := prepareBaldaCommand(cmd.Context(), false)
			if err != nil {
				return err
			}
			if err := validateBaldaApplicationFn(prepared); err != nil {
				return err
			}
			if err := preflightBaldaRuntimeFn(cmd.Context(), prepared); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "balda preflight: ok")
			return nil
		},
	}
	return cmd
}

func doctorCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:           "doctor",
		Short:         "Run full Balda operator checks",
		Long:          "Load Balda configuration, validate app wiring, preflight the configured provider runtime, and report overall readiness.",
		SilenceErrors: true,
		SilenceUsage:  true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			prepared, err := prepareBaldaCommand(cmd.Context(), false)
			if err != nil {
				return err
			}
			if err := validateBaldaApplicationFn(prepared); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "doctor: validate ok")
			if err := preflightBaldaRuntimeFn(cmd.Context(), prepared); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "doctor: preflight ok")
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "balda doctor: ok")
			return nil
		},
	}
	return cmd
}

func prepareBaldaCommand(ctx context.Context, requireUserReady bool) (preparedBaldaCommand, error) {
	prepared, err := loadBaldaCommandConfig(true)
	if err != nil {
		return preparedBaldaCommand{}, err
	}
	if err := os.MkdirAll(prepared.stateDir, 0o700); err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("create balda state dir: %w", err)
	}
	provider, err := state.Open(ctx, prepared.database)
	if err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("open balda state provider: %w", err)
	}
	defer func() { _ = provider.Close() }()
	ownerToken, err := loadOrCreateBaldaOwnerTokenFromProvider(ctx, provider)
	if err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("bootstrap balda owner token: %w", err)
	}
	if requireUserReady {
		server, err := prepared.baldaCfg.Balda.Backoffice.Resolve()
		if err != nil {
			return preparedBaldaCommand{}, err
		}
		runtime, err := backoffice.NewRuntime(backoffice.ResolvedConfig{Server: server, Database: prepared.database}, provider)
		if err != nil {
			return preparedBaldaCommand{}, err
		}
		if err := runtime.ValidateReady(ctx); err != nil {
			return preparedBaldaCommand{}, fmt.Errorf("backoffice user readiness: %w; run balda backoffice migrate-users or bootstrap-admin as needed", err)
		}
	}
	prepared.ownerToken = ownerToken
	return prepared, nil
}

func loadBaldaCommandConfig(requireChannel bool) (preparedBaldaCommand, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("getting working directory: %w", err)
	}

	runtimeLoadOpts := appconfig.RuntimeLoadOptions{
		WorkingDir: workingDir,
		ConfigDir:  viper.GetString("config_dir"),
		Profile:    viper.GetString("profile"),
	}

	var doc baldaConfigDocument
	_, err = appconfig.LoadConfigDocument(
		runtimeLoadOpts,
		appconfig.AppLoadOptions{
			AppName:            "balda",
			DefaultsYAML:       defaultBaldaConfig,
			UseDotConfigAppDir: true,
		},
		&doc,
	)
	if err != nil {
		return preparedBaldaCommand{}, err
	}
	if err := applyBaldaLogging(doc.Balda.Logger); err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("configure balda logging: %w", err)
	}

	baldaCfg := balda.Config{Balda: doc.Balda}
	if requireChannel {
		if err := validateBaldaChannelConfiguration(workingDir, baldaCfg); err != nil {
			return preparedBaldaCommand{}, err
		}
	}

	stateWorkingDir, err := paths.ResolveWorkingDir(baldaCfg.Balda.WorkingDir)
	if err != nil {
		return preparedBaldaCommand{}, err
	}
	stateDir, err := paths.ResolveStateDir(stateWorkingDir, baldaCfg.Balda.StateDir)
	if err != nil {
		return preparedBaldaCommand{}, fmt.Errorf("resolve balda state_dir: %w", err)
	}
	database, err := baldaCfg.Balda.Database.Resolve(stateWorkingDir, stateDir)
	if err != nil {
		return preparedBaldaCommand{}, err
	}
	if _, err := baldaCfg.Balda.Backoffice.Resolve(); err != nil {
		return preparedBaldaCommand{}, err
	}
	return preparedBaldaCommand{
		workingDir:      workingDir,
		stateDir:        stateDir,
		doc:             doc,
		baldaCfg:        baldaCfg,
		runtimeLoadOpts: runtimeLoadOpts,
		database:        database,
	}, nil
}

func validateBaldaChannelConfiguration(workingDir string, cfg balda.Config) error {
	if cfg.Balda.Telegram.Token == "" && !cfg.Balda.Zulip.Webhook.Enabled && !cfg.Balda.Mattermost.Enabled && !cfg.Balda.Slack.Enabled && !cfg.Balda.Slack.Agent.Enabled {
		return fmt.Errorf("at least one channel is required.\nFor Telegram:\n  - Environment: BALDA_TELEGRAM_TOKEN=<token>\n  - CWD .env: %s with BALDA_TELEGRAM_TOKEN=<token>\nFor Zulip: set balda.zulip.webhook.enabled=true (or BALDA_ZULIP_WEBHOOK_ENABLED=true)\nFor Mattermost: set balda.mattermost.enabled=true (or BALDA_MATTERMOST_ENABLED=true)\nFor Slack chat: set balda.slack.enabled=true (or BALDA_SLACK_ENABLED=true)\nFor Slack agent: set balda.slack.agent.enabled=true (or BALDA_SLACK_AGENT_ENABLED=true)", filepath.Join(workingDir, ".env"))
	}
	return nil
}

func validateBaldaApplication(prepared preparedBaldaCommand) error {
	if err := fx.ValidateApp(
		balda.Module(
			prepared.baldaCfg,
			prepared.doc.Runtime,
			prepared.ownerToken,
			prepared.runtimeLoadOpts,
			defaultBaldaConfig,
		),
	); err != nil {
		return fmt.Errorf("validate Balda app: %w", err)
	}
	return nil
}

func preflightBaldaRuntime(ctx context.Context, prepared preparedBaldaCommand) error {
	if err := balda.PreflightRuntime(ctx, prepared.baldaCfg, prepared.doc.Runtime, prepared.runtimeLoadOpts); err != nil {
		return fmt.Errorf("preflight Balda runtime: %w", err)
	}
	return nil
}
