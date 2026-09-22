package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/logging"
	"github.com/joho/godotenv"
	"github.com/spf13/cobra"
)

type rootOptions struct {
	configDir string
	profile   string
	debug     bool
	trace     bool
}

func execute() error {
	command := newRootCommand()
	return command.Execute()
}

func newRootCommand() *cobra.Command {
	options := &rootOptions{}
	command := &cobra.Command{
		Use:           "backoffice",
		Short:         "Balda Backoffice administration",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	command.PersistentFlags().StringVar(&options.configDir, "config-dir", "", "extra config root directory (highest priority)")
	command.PersistentFlags().StringVar(&options.profile, "profile", "", "config profile name")
	command.PersistentFlags().BoolVar(&options.debug, "debug", false, "enable debug logging")
	command.PersistentFlags().BoolVar(&options.trace, "trace", false, "enable trace logging")
	command.AddCommand(serveCommand(options))
	command.AddCommand(validateCommand(options))
	command.AddCommand(migrateUsersCommand(options))
	command.AddCommand(bootstrapAdminCommand(options))
	return command
}

func serveCommand(options *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "Serve the Backoffice web application",
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := loadConfig(options)
			if err != nil {
				return err
			}
			runtime, err := backoffice.OpenRuntime(command.Context(), config)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			ctx, cancel := signal.NotifyContext(command.Context(), syscall.SIGINT, syscall.SIGTERM)
			defer cancel()
			return runtime.Serve(ctx)
		},
	}
}

func validateCommand(options *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "validate",
		Short: "Validate Backoffice configuration, database, migration, and bootstrap state",
		RunE: func(command *cobra.Command, _ []string) error {
			config, err := loadConfig(options)
			if err != nil {
				return err
			}
			runtime, err := backoffice.OpenRuntime(command.Context(), config)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			if err := runtime.ValidateReady(command.Context()); err != nil {
				return err
			}
			_, _ = fmt.Fprintln(command.OutOrStdout(), "backoffice validate: ok")
			return nil
		},
	}
}

func migrateUsersCommand(options *rootOptions) *cobra.Command {
	var outputPath string
	var primarySubject string
	command := &cobra.Command{
		Use:   "migrate-users",
		Short: "Migrate legacy owner and collaborators into canonical Backoffice users",
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(outputPath) == "" {
				return fmt.Errorf("--credentials-output is required")
			}
			config, err := loadConfig(options)
			if err != nil {
				return err
			}
			runtime, err := backoffice.OpenRuntime(command.Context(), config)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			result, err := runtime.MigrateUsers(command.Context(), outputPath, primarySubject)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "user migration applied=%t users=%d bindings=%d credentials_output=%s\n", result.Applied, result.UserCount, result.BindingCount, outputPath)
			return nil
		},
	}
	command.Flags().StringVar(&outputPath, "credentials-output", "", "exclusive 0600 output file for generated temporary credentials")
	command.Flags().StringVar(&primarySubject, "primary-subject", "", "explicit legacy owner subject to select as primary")
	return command
}

func bootstrapAdminCommand(options *rootOptions) *cobra.Command {
	var input backoffice.BootstrapInput
	command := &cobra.Command{
		Use:   "bootstrap-admin",
		Short: "Create or explicitly reset administrator credentials using a password from stdin",
		RunE: func(command *cobra.Command, _ []string) error {
			password, err := readPassword(command.InOrStdin())
			if err != nil {
				return err
			}
			defer zero(password)
			input.Password = password
			config, err := loadConfig(options)
			if err != nil {
				return err
			}
			runtime, err := backoffice.OpenRuntime(command.Context(), config)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			result, err := runtime.BootstrapAdmin(command.Context(), input)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "administrator ready: id=%s username=%s created=%t\n", result.UserID, result.Username, result.Created)
			return nil
		},
	}
	command.Flags().StringVar(&input.UserID, "user-id", "", "explicit existing administrator user ID")
	command.Flags().StringVar(&input.Username, "username", "", "username for a fresh administrator")
	command.Flags().StringVar(&input.DisplayName, "display-name", "", "display name for a fresh administrator")
	command.Flags().BoolVar(&input.Reset, "reset", false, "explicitly replace an existing usable credential and revoke all sessions")
	return command
}

func loadConfig(options *rootOptions) (backoffice.ResolvedConfig, error) {
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return backoffice.ResolvedConfig{}, fmt.Errorf("load .env: %w", err)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return backoffice.ResolvedConfig{}, fmt.Errorf("get working directory: %w", err)
	}
	config, err := backoffice.LoadConfig(backoffice.LoadOptions{
		WorkingDir: workingDir, ConfigDir: options.configDir, Profile: options.profile,
	})
	if err != nil {
		return backoffice.ResolvedConfig{}, err
	}
	level := strings.TrimSpace(config.Balda.Logger.Level)
	if options.debug {
		level = logging.LevelDebug
	}
	if options.trace {
		level = logging.LevelTrace
	}
	if err := logging.Init(logging.WithLevel(level), logging.WithJson(!config.Balda.Logger.Pretty)); err != nil {
		return backoffice.ResolvedConfig{}, fmt.Errorf("configure Backoffice logging: %w", err)
	}
	return config, nil
}

func readPassword(reader io.Reader) ([]byte, error) {
	if file, ok := reader.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			return nil, fmt.Errorf("password must be provided on stdin without terminal echo")
		}
	}
	data, err := io.ReadAll(io.LimitReader(reader, userpassword.MaxLength+3))
	if err != nil {
		return nil, fmt.Errorf("read password from stdin: %w", err)
	}
	data = bytes.TrimSuffix(data, []byte("\n"))
	data = bytes.TrimSuffix(data, []byte("\r"))
	if bytes.ContainsAny(data, "\r\n") || len(data) < userpassword.MinLength || len(data) > userpassword.MaxLength {
		zero(data)
		return nil, userpassword.ErrInvalidPassword
	}
	return data, nil
}

func zero(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
