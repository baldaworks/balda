package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/browseredge"
	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	"github.com/baldaworks/balda/internal/apps/backoffice/standalone"
	"github.com/baldaworks/runtime/v2/appconfig"
	"github.com/spf13/cobra"
)

const shutdownTimeout = 10 * time.Second

var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

const defaultConfig = `runtime:
  providers:
    agent:
      type: opencode_acp
      opencode_acp:
        model: ""
balda:
  http:
    base_url: http://127.0.0.1:8095
    base_path: ""
  backoffice:
    listen_addr: 127.0.0.1:8095
  management:
    enabled: false
    listen_addr: 127.0.0.1:8096
    operator_token: ""
`

type options struct {
	configDir string
	profile   string
}

func main() {
	if err := execute(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}

func execute() error {
	var opts options
	root := &cobra.Command{Use: "balda-backoffice", Short: "Standalone Balda Backoffice", SilenceErrors: true, SilenceUsage: true,
		Version: fmt.Sprintf("balda-backoffice %s (commit %s, built %s)", version, commit, date)}
	root.SetVersionTemplate("{{.Version}}\n")
	root.PersistentFlags().StringVar(&opts.configDir, "config-dir", "", "extra config root directory")
	root.PersistentFlags().StringVar(&opts.profile, "profile", "", "config profile name")
	root.AddCommand(&cobra.Command{Use: "validate", RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := prepare(cmd.Context(), opts, false)
		if err == nil {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "balda-backoffice validate: ok")
		}
		return err
	}})
	root.AddCommand(&cobra.Command{Use: "preflight", RunE: func(cmd *cobra.Command, _ []string) error {
		_, err := prepare(cmd.Context(), opts, true)
		if err == nil {
			_, _ = fmt.Fprintln(cmd.OutOrStdout(), "balda-backoffice preflight: ok")
		}
		return err
	}})
	root.AddCommand(&cobra.Command{Use: "start", RunE: func(cmd *cobra.Command, _ []string) error { return start(cmd.Context(), opts) }})
	return root.Execute()
}

type prepared struct {
	config standalone.ResolvedConfig
	client *managementclient.Client
}

func prepare(ctx context.Context, opts options, network bool) (prepared, error) {
	workingDir, err := os.Getwd()
	if err != nil {
		return prepared{}, fmt.Errorf("resolve working directory: %w", err)
	}
	document, _, err := standalone.LoadConfigDocument(appconfig.RuntimeLoadOptions{WorkingDir: workingDir, ConfigDir: opts.configDir, Profile: opts.profile}, []byte(defaultConfig))
	if err != nil {
		return prepared{}, fmt.Errorf("load configuration: %w", err)
	}
	resolved, err := document.Balda.Resolve()
	if err != nil {
		return prepared{}, err
	}
	client, err := managementclient.New(managementclient.Config{BaseURL: resolved.CoreURL, OperatorToken: resolved.OperatorToken})
	if err != nil {
		return prepared{}, err
	}
	if network {
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := client.Compatibility(checkCtx); err != nil {
			return prepared{}, fmt.Errorf("check Core compatibility: %w", err)
		}
	}
	return prepared{config: resolved, client: client}, nil
}

func start(parent context.Context, opts options) error {
	ctx, cancel := signal.NotifyContext(parent, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	prepared, err := prepare(ctx, opts, true)
	if err != nil {
		return err
	}
	edge, err := browseredge.New(prepared.client, browseredge.Config{PublicOrigin: prepared.config.PublicOrigin, MountPath: prepared.config.MountPath, WebhookURLPrefix: prepared.config.WebhookURLPrefix})
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", prepared.config.ListenAddr)
	if err != nil {
		return fmt.Errorf("bind Backoffice listener: %w", err)
	}
	server := &http.Server{Handler: edge.Handler(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	select {
	case err = <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("serve Backoffice: %w", err)
	case <-ctx.Done():
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("stop Backoffice: %w", err)
		}
		err = <-done
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return fmt.Errorf("serve Backoffice: %w", err)
		}
		return nil
	}
}
