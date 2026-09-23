package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/spf13/cobra"
)

func backofficeCommand() *cobra.Command {
	command := &cobra.Command{Use: "backoffice", Short: "Maintain Backoffice users"}
	command.AddCommand(bootstrapAdminCommand(), migrateUsersCommand())
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

func migrateUsersCommand() *cobra.Command {
	var outputPath, primarySubject string
	command := &cobra.Command{
		Use:   "migrate-users",
		Short: "Convert legacy owner and collaborators into canonical users",
		RunE: func(command *cobra.Command, _ []string) error {
			if strings.TrimSpace(outputPath) == "" {
				return fmt.Errorf("--credentials-output is required")
			}
			runtime, err := openBackofficeMaintenance(command)
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

func bootstrapAdminCommand() *cobra.Command {
	var input backoffice.BootstrapInput
	command := &cobra.Command{
		Use:   "bootstrap-admin",
		Short: "Create or reset administrator credentials using a password from stdin",
		RunE: func(command *cobra.Command, _ []string) error {
			password, err := readBackofficePassword(command.InOrStdin())
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
			return nil
		},
	}
	command.Flags().StringVar(&input.UserID, "user-id", "", "existing administrator user ID")
	command.Flags().StringVar(&input.Username, "username", "", "username for a fresh administrator")
	command.Flags().StringVar(&input.DisplayName, "display-name", "", "display name for a fresh administrator")
	command.Flags().BoolVar(&input.Reset, "reset", false, "replace usable credentials and revoke browser sessions")
	return command
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
