package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/spf13/cobra"
)

func backofficeCommand() *cobra.Command {
	command := &cobra.Command{Use: "backoffice", Short: "Maintain and review Backoffice"}
	command.AddCommand(bootstrapAdminCommand(), recover2FACommand(), backofficeQACommand())
	return command
}

func backofficeQACommand() *cobra.Command {
	command := &cobra.Command{Use: "qa", Short: "Review synthetic Backoffice screens"}
	var listenAddr string
	serve := &cobra.Command{
		Use:   "serve",
		Short: "Serve the local Backoffice QA gallery",
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, stop := signal.NotifyContext(command.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			return backoffice.ServeQA(ctx, listenAddr, func(address string) {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "Backoffice QA: http://%s/qa/ui/\n", address)
			})
		},
	}
	serve.Flags().StringVar(&listenAddr, "listen", "127.0.0.1:8096", "loopback address for the QA gallery")
	command.AddCommand(serve)
	return command
}

func openBackofficeMaintenance(command *cobra.Command) (*backoffice.Runtime, error) {
	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		return nil, err
	}
	server, err := prepared.baldaCfg.Balda.Backoffice.Resolve()
	if err != nil {
		return nil, err
	}
	return backoffice.OpenRuntime(command.Context(), backoffice.ResolvedConfig{
		Server: server, Database: prepared.database,
	})
}

func bootstrapAdminCommand() *cobra.Command {
	var input backoffice.BootstrapInput
	command := &cobra.Command{
		Use:   "bootstrap-admin",
		Short: "Create or reset administrator credentials with a generated password",
		RunE: func(command *cobra.Command, _ []string) error {
			password, generated, err := bootstrapAdminPassword(command.InOrStdin())
			if err != nil {
				return err
			}
			defer zeroPassword(password)
			input.Password = password
			runtime, err := openBackofficeMaintenance(command)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			result, err := runtime.BootstrapAdmin(command.Context(), input)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(command.OutOrStdout(), "administrator ready: id=%s username=%s created=%t\n", result.UserID, result.Username, result.Created)
			if generated {
				_, _ = fmt.Fprintf(command.OutOrStdout(), "administrator password: %s\n", password)
			}
			return nil
		},
	}
	command.Flags().StringVar(&input.UserID, "user-id", "", "existing administrator user ID")
	command.Flags().StringVar(&input.Username, "username", "", "username for a fresh administrator (must be superuser)")
	command.Flags().StringVar(&input.DisplayName, "display-name", "", "display name for a fresh administrator (must be superuser)")
	command.Flags().BoolVar(&input.Reset, "reset", false, "replace usable credentials and revoke browser sessions")
	return command
}

func recover2FACommand() *cobra.Command {
	var input backoffice.RecoveryInput
	command := &cobra.Command{
		Use: "recover-2fa", Short: "Disable a lost administrator factor and revoke browser sessions",
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if !input.Confirm {
				return fmt.Errorf("recover-2fa requires --confirm")
			}
			runtime, err := openBackofficeMaintenance(command)
			if err != nil {
				return err
			}
			defer func() { _ = runtime.Close() }()
			result, err := runtime.Recover2FA(command.Context(), input)
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(command.OutOrStdout(), "2FA disabled for %s; browser sessions revoked\n", result.Username)
			return err
		},
	}
	command.Flags().StringVar(&input.Username, "username", "", "exact normalized administrator username")
	command.Flags().BoolVar(&input.Confirm, "confirm", false, "explicitly confirm disabling 2FA")
	_ = command.MarkFlagRequired("username")
	return command
}

var baldaGenerateAdminPassword = userpassword.Generate

func bootstrapAdminPassword(reader io.Reader) ([]byte, bool, error) {
	if file, ok := reader.(*os.File); ok {
		if info, err := file.Stat(); err == nil && info.Mode()&os.ModeCharDevice != 0 {
			password, err := baldaGenerateAdminPassword()
			return password, true, err
		}
	}
	data, err := io.ReadAll(io.LimitReader(reader, userpassword.MaxLength+3))
	if err != nil {
		return nil, false, fmt.Errorf("read password from stdin: %w", err)
	}
	if len(data) == 0 {
		password, err := baldaGenerateAdminPassword()
		return password, true, err
	}
	password, err := readBackofficePassword(bytes.NewReader(data))
	return password, false, err
}

func readBackofficePassword(reader io.Reader) ([]byte, error) {
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
		zeroPassword(data)
		return nil, userpassword.ErrInvalidPassword
	}
	return data, nil
}

func zeroPassword(value []byte) {
	for i := range value {
		value[i] = 0
	}
}
