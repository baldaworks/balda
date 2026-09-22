package balda

import (
	"context"
	"testing"

	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/stretchr/testify/require"
	"go.uber.org/fx"
)

func TestValidateApp(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	runGitForBalda(t, ctx, workingDir, "init")

	cfg := Config{
		Balda: BaldaConfig{
			Telegram: TelegramConfig{
				Token: "test-token",
			},
			WorkingDir: workingDir,
			StateDir:   ".config/balda",
			Execution:  ExecutionConfig{},
			Features: FeaturesConfig{Attachments: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
				MaxTotalBytes:      defaultAttachmentMaxTotalBytes,
			}},
			Workspace: WorkspaceConfig{
				Mode: string(WorkspaceModeAuto),
			},
		},
	}

	err := fx.ValidateApp(
		Module(
			cfg,
			runtimeconfig.RuntimeConfig{},
			"test-owner-token",
			runtimeconfig.RuntimeLoadOptions{WorkingDir: workingDir},
			nil,
		),
	)

	require.NoError(t, err)
}

func TestValidateApp_InvalidTelegramFormattingModeFails(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	runGitForBalda(t, ctx, workingDir, "init")

	cfg := Config{
		Balda: BaldaConfig{
			Telegram: TelegramConfig{
				Token:          "test-token",
				FormattingMode: "markdown",
			},
			WorkingDir: workingDir,
			StateDir:   ".config/balda",
			Execution:  ExecutionConfig{},
			Features: FeaturesConfig{Attachments: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
				MaxTotalBytes:      defaultAttachmentMaxTotalBytes,
			}},
			Workspace: WorkspaceConfig{
				Mode: string(WorkspaceModeAuto),
			},
		},
	}

	err := fx.ValidateApp(
		Module(
			cfg,
			runtimeconfig.RuntimeConfig{},
			"test-owner-token",
			runtimeconfig.RuntimeLoadOptions{WorkingDir: workingDir},
			nil,
		),
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid balda.telegram.formatting_mode")
}

func TestValidateApp_InvalidAttachmentLimitsFail(t *testing.T) {
	ctx := context.Background()
	workingDir := t.TempDir()
	runGitForBalda(t, ctx, workingDir, "init")

	cfg := Config{
		Balda: BaldaConfig{
			Telegram:   TelegramConfig{Token: "test-token"},
			WorkingDir: workingDir,
			StateDir:   ".config/balda",
			Features: FeaturesConfig{Attachments: AttachmentsConfig{
				MaxFilesPerMessage: defaultAttachmentMaxFilesPerMessage,
				MaxFileBytes:       defaultAttachmentMaxFileBytes,
				MaxTotalBytes:      defaultAttachmentMaxFileBytes - 1,
			}},
			Workspace: WorkspaceConfig{Mode: string(WorkspaceModeAuto)},
		},
	}

	err := fx.ValidateApp(
		Module(
			cfg,
			runtimeconfig.RuntimeConfig{},
			"test-owner-token",
			runtimeconfig.RuntimeLoadOptions{WorkingDir: workingDir},
			nil,
		),
	)

	require.Error(t, err)
	require.Contains(t, err.Error(), "max_total_bytes must be at least max_file_bytes")
}
