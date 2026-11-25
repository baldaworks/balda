package main

import (
	"fmt"
	"path/filepath"
	"testing"

	baldaapp "github.com/baldaworks/balda/internal/apps/balda"
	"github.com/baldaworks/runtime/v2/appconfig"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const testApplicationSecurityKey = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

func TestLoadConfigDocumentAppliesApplicationSecurityKeyOverride(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("BALDA_SECURITY_KEY", testApplicationSecurityKey)
	viper.Reset()
	t.Cleanup(viper.Reset)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), "runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n"); err != nil {
		t.Fatal(err)
	}
	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.doc.Balda.Security.Key != testApplicationSecurityKey || prepared.baldaCfg.Balda.Security.Key != testApplicationSecurityKey {
		t.Fatal("application security key override was not applied")
	}
}

func TestLoadConfigDocumentAppliesApplicationSecurityKeyFromYAML(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	viper.Reset()
	t.Cleanup(viper.Reset)
	config := fmt.Sprintf("runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n  security:\n    key: %q\n", testApplicationSecurityKey)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), config); err != nil {
		t.Fatal(err)
	}

	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.baldaCfg.Balda.Security.Key != testApplicationSecurityKey {
		t.Fatal("balda.security.key YAML value was not applied")
	}
}

func TestCommandsRejectInvalidSecurityKey(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	t.Setenv("BALDA_SECURITY_KEY", "not-base64")
	if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), acpTestRuntime); err != nil {
		t.Fatal(err)
	}
	for _, cmd := range []*cobra.Command{startCommand(), validateCommand()} {
		t.Run(cmd.Name(), func(t *testing.T) {
			err := cmd.RunE(cmd, nil)
			if err == nil || err.Error() != "balda.security.key failed validation rule encryption_key" {
				t.Fatalf("unexpected validation error: %v", err)
			}
		})
	}
}

func TestConfigValidatesEncryptionKey(t *testing.T) {
	for _, tc := range []struct {
		key   string
		valid bool
	}{{"", true}, {testApplicationSecurityKey, true}, {"YQ==", false}, {"invalid", false}} {
		cfg := baldaapp.Config{Balda: baldaapp.BaldaConfig{Security: baldaapp.SecurityConfig{Key: tc.key}}}
		err := cfg.Validate()
		if (err == nil) != tc.valid {
			t.Fatal("unexpected key validation outcome")
		}
	}
}

func TestSecurityKeyEnvPrefixAndPrecedence(t *testing.T) {
	for _, value := range []string{"", testApplicationSecurityKey} {
		t.Run(fmt.Sprintf("length_%d", len(value)), func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("CUSTOM_SECURITY_KEY", value)
			if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), acpTestRuntime+"balda:\n  security:\n    key: file-value\n"); err != nil {
				t.Fatal(err)
			}
			var doc baldaConfigDocument
			_, err := loadBaldaDocument(appconfig.RuntimeLoadOptions{WorkingDir: dir}, appconfig.AppLoadOptions{AppName: "balda", EnvPrefix: "CUSTOM", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true}, &doc)
			if err != nil {
				t.Fatal(err)
			}
			if doc.Balda.Security.Key != value {
				t.Fatal("security key environment precedence lost")
			}
		})
	}
}

func TestLoadBaldaDocumentBindsApplicationPaths(t *testing.T) {
	dir := t.TempDir()
	if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), acpTestRuntime+"balda:\n  security:\n    key: null\n"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("BALDA_SECURITY_KEY", testApplicationSecurityKey)
	t.Setenv("BALDA_TELEGRAM_TOKEN", "telegram-token")
	t.Setenv("BALDA_SLACK_BOT_TOKEN", "slack-token")
	t.Setenv("BALDA_HTTP_BASE_URL", "https://example.test")
	var doc baldaConfigDocument
	_, err := loadBaldaDocument(appconfig.RuntimeLoadOptions{WorkingDir: dir}, appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true}, &doc)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balda.Security.Key != testApplicationSecurityKey || doc.Balda.Telegram.Token != "telegram-token" || doc.Balda.Slack.BotToken != "slack-token" || doc.Balda.HTTP.BaseURL != "https://example.test" {
		t.Fatal("application environment overrides did not reach the decoded config")
	}
}
