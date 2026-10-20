package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcliapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/stateowner"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

type skillCLI interface {
	List(ctx context.Context) (skillcliapp.Inventory, error)
	Show(ctx context.Context, selector string) (skillcliapp.Entry, error)
	Install(ctx context.Context, request skillcliapp.InstallRequest, confirm skillcliapp.Confirm) (skillcliapp.Result, error)
	Update(ctx context.Context, selector string, confirm skillcliapp.Confirm) (skillcliapp.Result, error)
	Change(ctx context.Context, operation skillcmd.Operation, selector string, confirm skillcliapp.Confirm) (skillcliapp.Result, error)
	Refresh(ctx context.Context, confirm skillcliapp.Confirm) (skillcliapp.Result, error)
	Recover(ctx context.Context) (skillcliapp.Result, error)
}

var prepareSkillCLI = prepareSkillCLIApplication

func skillsCommand() *cobra.Command {
	cmd := &cobra.Command{Use: "skill", Short: "Inspect and manage standalone skills"}
	cmd.AddCommand(skillsListCommand())
	cmd.AddCommand(skillsShowCommand())
	cmd.AddCommand(skillsInstallCommand())
	cmd.AddCommand(skillsUpdateCommand())
	cmd.AddCommand(skillsChangeCommand(skillcmd.OperationEnable))
	cmd.AddCommand(skillsChangeCommand(skillcmd.OperationDisable))
	cmd.AddCommand(skillsChangeCommand(skillcmd.OperationRemove))
	cmd.AddCommand(skillsRefreshCommand())
	cmd.AddCommand(skillsRecoverCommand())
	return cmd
}

func skillsListCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List visible standalone skills",
		Args:  skillArgs(0, &jsonOutput),
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), false)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			inventory, err := app.List(cmd.Context())
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			if jsonOutput {
				return writeSkillJSON(cmd.OutOrStdout(), skillcliapp.Envelope{OK: true, Inventory: &inventory})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), skillcliapp.RenderInventoryPlain(inventory))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit stable JSON output")
	return cmd
}

func skillsShowCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "show <selector>",
		Short: "Show one standalone skill",
		Args:  skillArgs(1, &jsonOutput),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), false)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			entry, err := app.Show(cmd.Context(), args[0])
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			if jsonOutput {
				return writeSkillJSON(cmd.OutOrStdout(), skillcliapp.Envelope{OK: true, Entry: &entry})
			}
			_, err = fmt.Fprintln(cmd.OutOrStdout(), skillcliapp.RenderEntryPlain(entry))
			return err
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit stable JSON output")
	return cmd
}

func skillsInstallCommand() *cobra.Command {
	var ref, path string
	var yes, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "install <source>",
		Short: "Review and install one standalone skill",
		Args:  skillArgs(1, &jsonOutput),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), true)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			var review skillcliapp.Review
			result, err := app.Install(cmd.Context(), skillcliapp.InstallRequest{Source: args[0], Ref: ref, Path: path}, skillConfirmation(cmd, yes, jsonOutput, &review))
			return writeSkillMutation(cmd, jsonOutput, review, result, err)
		},
	}
	cmd.Flags().StringVar(&ref, "ref", "", "Git ref to review")
	cmd.Flags().StringVar(&path, "path", "", "relative path to one skill")
	addSkillMutationFlags(cmd, &yes, &jsonOutput)
	return cmd
}

func skillsUpdateCommand() *cobra.Command {
	var yes, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "update <selector>",
		Short: "Review and update one managed skill",
		Args:  skillArgs(1, &jsonOutput),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), true)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			var review skillcliapp.Review
			result, err := app.Update(cmd.Context(), args[0], skillConfirmation(cmd, yes, jsonOutput, &review))
			return writeSkillMutation(cmd, jsonOutput, review, result, err)
		},
	}
	addSkillMutationFlags(cmd, &yes, &jsonOutput)
	return cmd
}

func skillsChangeCommand(operation skillcmd.Operation) *cobra.Command {
	var yes, jsonOutput bool
	cmd := &cobra.Command{
		Use:   string(operation) + " <selector>",
		Short: strings.ToUpper(string(operation[:1])) + string(operation[1:]) + " one managed skill",
		Args:  skillArgs(1, &jsonOutput),
		RunE: func(cmd *cobra.Command, args []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), true)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			var review skillcliapp.Review
			result, err := app.Change(cmd.Context(), operation, args[0], skillConfirmation(cmd, yes, jsonOutput, &review))
			return writeSkillMutation(cmd, jsonOutput, review, result, err)
		},
	}
	addSkillMutationFlags(cmd, &yes, &jsonOutput)
	return cmd
}

func skillsRefreshCommand() *cobra.Command {
	var yes, jsonOutput bool
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Rebuild eligible global skill roots",
		Args:  skillArgs(0, &jsonOutput),
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), true)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			var review skillcliapp.Review
			result, err := app.Refresh(cmd.Context(), skillConfirmation(cmd, yes, jsonOutput, &review))
			return writeSkillMutation(cmd, jsonOutput, review, result, err)
		},
	}
	addSkillMutationFlags(cmd, &yes, &jsonOutput)
	return cmd
}

func skillsRecoverCommand() *cobra.Command {
	var jsonOutput bool
	cmd := &cobra.Command{
		Use:   "recover",
		Short: "Retry accepted pending skill publication",
		Args:  skillArgs(0, &jsonOutput),
		RunE: func(cmd *cobra.Command, _ []string) error {
			app, cleanup, err := prepareSkillCLI(cmd.Context(), true)
			if err != nil {
				return writeSkillError(cmd, jsonOutput, err)
			}
			defer cleanup()
			result, err := app.Recover(cmd.Context())
			return writeSkillMutation(cmd, jsonOutput, skillcliapp.Review{}, result, err)
		},
	}
	cmd.Flags().BoolVar(&jsonOutput, "json", false, "emit stable JSON output")
	return cmd
}

func addSkillMutationFlags(cmd *cobra.Command, yes, jsonOutput *bool) {
	cmd.Flags().BoolVar(yes, "yes", false, "accept the reviewed mutation without prompting")
	cmd.Flags().BoolVar(jsonOutput, "json", false, "emit stable JSON output; mutations also require --yes")
}

func skillArgs(want int, jsonOutput *bool) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) == want {
			return nil
		}
		return writeSkillError(cmd, *jsonOutput, &skillcliapp.Error{Code: skillcliapp.CodeInvalid})
	}
}

func skillConfirmation(cmd *cobra.Command, yes, jsonOutput bool, captured *skillcliapp.Review) skillcliapp.Confirm {
	return func(review skillcliapp.Review) (bool, error) {
		*captured = review
		if jsonOutput {
			if !yes {
				return false, errors.New("JSON mutation requires --yes")
			}
			return true, nil
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), skillcliapp.RenderReviewPlain(review)); err != nil {
			return false, err
		}
		if yes {
			return true, nil
		}
		if !terminalReader(cmd.InOrStdin()) {
			return false, errors.New("non-interactive confirmation requires --yes")
		}
		if _, err := fmt.Fprint(cmd.OutOrStdout(), "Proceed? [y/N] "); err != nil {
			return false, err
		}
		line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return false, err
		}
		answer := strings.ToLower(strings.TrimSpace(line))
		return answer == "y" || answer == "yes", nil
	}
}

func terminalReader(reader io.Reader) bool {
	file, ok := reader.(*os.File)
	return ok && term.IsTerminal(int(file.Fd()))
}

func writeSkillMutation(cmd *cobra.Command, jsonOutput bool, review skillcliapp.Review, result skillcliapp.Result, err error) error {
	if err != nil {
		return writeSkillError(cmd, jsonOutput, err)
	}
	if result.Accepted && result.Pending {
		public := &skillcliapp.Error{Code: skillcliapp.CodePublicationPending}
		cmd.Root().SilenceErrors = true
		cmd.Root().SilenceUsage = true
		if jsonOutput {
			envelope := skillcliapp.Envelope{OK: false, Result: &result, Error: public}
			if review.Operation != "" {
				envelope.Review = &review
			}
			if encodeErr := writeSkillJSON(cmd.OutOrStdout(), envelope); encodeErr != nil {
				return encodeErr
			}
		} else if _, writeErr := fmt.Fprintln(cmd.OutOrStdout(), skillcliapp.RenderResultPlain(result)); writeErr != nil {
			return writeErr
		}
		return &printedSkillError{public: public}
	}
	if jsonOutput {
		envelope := skillcliapp.Envelope{OK: true, Result: &result}
		if review.Operation != "" {
			envelope.Review = &review
		}
		return writeSkillJSON(cmd.OutOrStdout(), envelope)
	}
	_, err = fmt.Fprintln(cmd.OutOrStdout(), skillcliapp.RenderResultPlain(result))
	return err
}

func writeSkillError(cmd *cobra.Command, jsonOutput bool, err error) error {
	public := publicSkillError(err)
	cmd.Root().SilenceUsage = true
	if !jsonOutput {
		return public
	}
	if encodeErr := writeSkillJSON(cmd.OutOrStdout(), skillcliapp.Envelope{OK: false, Error: public}); encodeErr != nil {
		return encodeErr
	}
	cmd.Root().SilenceErrors = true
	cmd.Root().SilenceUsage = true
	return &printedSkillError{public: public}
}

func publicSkillError(err error) *skillcliapp.Error {
	if public, ok := skillcliapp.AsError(err); ok {
		return public
	}
	switch {
	case errors.Is(err, stateowner.ErrActive):
		return &skillcliapp.Error{Code: skillcliapp.CodeRuntimeActive}
	default:
		return &skillcliapp.Error{Code: skillcliapp.CodeUnavailable}
	}
}

func writeSkillJSON(writer io.Writer, value skillcliapp.Envelope) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

type printedSkillError struct{ public *skillcliapp.Error }

func (e *printedSkillError) Error() string { return e.public.Error() }
func (e *printedSkillError) ExitCode() int { return 1 }

func prepareSkillCLIApplication(ctx context.Context, mutation bool) (skillCLI, func(), error) {
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
	previousCleanup := cleanup
	cleanup = func() {
		_ = provider.Close()
		previousCleanup()
	}
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
	if err := catalog.LoadMaintenanceSnapshot(ctx); err != nil {
		cleanup()
		return nil, nil, err
	}
	var lifecycle *skillapp.Service
	if mutation {
		lifecycle, err = skillapp.New(prepared.stateDir, provider.SkillManagement(), catalog)
	} else {
		lifecycle, err = skillapp.NewReadOnly(provider.SkillManagement(), catalog)
	}
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	app, err := skillcliapp.New(lifecycle)
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	return app, cleanup, nil
}
