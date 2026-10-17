package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/runtime/v2/appconfig"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func TestLoadACPStartupTimeout(t *testing.T) {
	for _, tt := range []struct {
		name, setting, profile, env string
		want                        time.Duration
		invalid                     bool
	}{
		{name: "omitted", want: time.Minute},
		{name: "default only env discovery", env: "30s", want: 30 * time.Second},
		{name: "file", setting: `"30s"`, want: 30 * time.Second},
		{name: "profile over file", setting: `"30s"`, profile: "2m", want: 2 * time.Minute},
		{name: "env over profile and file", setting: `"30s"`, profile: "2m", env: "3m", want: 3 * time.Minute},
		{name: "empty env retains file", setting: `"2m"`, env: "", want: 2 * time.Minute},
		{name: "empty env retains profile", setting: `"30s"`, profile: "2m", env: "", want: 2 * time.Minute},
		{name: "empty", setting: `""`, want: time.Minute},
		{name: "whitespace", setting: `"  "`, want: time.Minute},
		{name: "null", setting: "null", want: time.Minute},
		{name: "whitespace env selects default", setting: `"2m"`, env: "  ", want: time.Minute},
		{name: "trim", setting: `" 2m "`, want: 2 * time.Minute},
		{name: "positive nanosecond", setting: `"1ns"`, want: time.Nanosecond},
		{name: "maximum duration", setting: `"9223372036854775807ns"`, want: time.Duration(1<<63 - 1)},
		{name: "zero", setting: "0", invalid: true},
		{name: "zero duration", setting: `"0s"`, invalid: true},
		{name: "negative", setting: `"-1s"`, invalid: true},
		{name: "malformed", setting: `"bad"`, invalid: true},
		{name: "overflow", setting: `"9223372036854775808ns"`, invalid: true},
		{name: "unitless", setting: "30", invalid: true},
		{name: "invalid env wins valid file", setting: `"2m"`, env: "bad", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Setenv("BALDA_ACP_STARTUP_TIMEOUT", tt.env)
			content := acpTestRuntime + "balda:\n  provider: test-provider\n"
			if tt.setting != "" {
				content += "  acp_startup_timeout: " + tt.setting + "\n"
			}
			profile := ""
			if tt.profile != "" {
				profile = "selected"
				content += "profiles:\n  selected:\n    balda:\n      acp_startup_timeout: " + tt.profile + "\n"
			}
			if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), content); err != nil {
				t.Fatal(err)
			}
			var doc baldaConfigDocument
			if _, err := appconfig.LoadConfigDocument(
				appconfig.RuntimeLoadOptions{WorkingDir: dir, Profile: profile},
				appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
				&doc,
			); err != nil {
				t.Fatal(err)
			}
			got, err := doc.Balda.ResolveACPStartupTimeout()
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "balda.acp_startup_timeout") {
					t.Fatalf("ResolveACPStartupTimeout() = %s, %v; want key-specific validation error", got, err)
				}
			} else if err != nil || got != tt.want {
				t.Fatalf("ResolveACPStartupTimeout() = %s, %v; want %s", got, err, tt.want)
			}
			t.Chdir(dir)
			oldProfile := viper.Get("profile")
			viper.Set("profile", profile)
			t.Cleanup(func() { viper.Set("profile", oldProfile) })
			prepared, err := loadBaldaCommandConfig(false)
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "balda.acp_startup_timeout") {
					t.Fatalf("loadBaldaCommandConfig() error = %v; want key-specific validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err = prepared.baldaCfg.Balda.ResolveACPStartupTimeout()
			if err != nil || got != tt.want {
				t.Fatalf("command startup timeout = %s, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestLoadBaldaCommandACPStartupEnvWithNullFile(t *testing.T) {
	for _, tt := range []struct {
		name, env string
		want      time.Duration
		invalid   bool
	}{
		{name: "nonempty", env: "30s", want: 30 * time.Second},
		{name: "empty", want: time.Minute},
		{name: "invalid", env: "0s", invalid: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("BALDA_ACP_STARTUP_TIMEOUT", tt.env)
			if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), acpTestRuntime+`balda:
  acp_startup_timeout: null
`); err != nil {
				t.Fatal(err)
			}
			prepared, err := loadBaldaCommandConfig(false)
			if tt.invalid {
				if err == nil || !strings.Contains(err.Error(), "balda.acp_startup_timeout") {
					t.Fatalf("loadBaldaCommandConfig() error = %v; want key-specific validation error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := prepared.baldaCfg.Balda.ResolveACPStartupTimeout()
			if err != nil || got != tt.want {
				t.Fatalf("command startup timeout = %s, %v; want %s", got, err, tt.want)
			}
		})
	}
}

func TestCommandsRejectInvalidACPStartupBeforeStartup(t *testing.T) {
	for _, newCommand := range []func() *cobra.Command{startCommand, validateCommand, preflightCommand, doctorCommand} {
		cmd := newCommand()
		t.Run(cmd.Name(), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)
			t.Setenv("BALDA_ACP_STARTUP_TIMEOUT", "0s")
			if err := writeFile(filepath.Join(dir, ".config", "balda", "config.yaml"), acpTestRuntime+`balda:
  telegram:
    token: test-token
  state_dir: state
`); err != nil {
				t.Fatal(err)
			}
			oldValidate, oldPreflight := validateBaldaApplicationFn, preflightBaldaRuntimeFn
			t.Cleanup(func() {
				validateBaldaApplicationFn, preflightBaldaRuntimeFn = oldValidate, oldPreflight
			})
			validateBaldaApplicationFn = func(preparedBaldaCommand) error {
				t.Fatal("application wiring validation called with invalid startup timeout")
				return nil
			}
			preflightBaldaRuntimeFn = func(_ context.Context, _ preparedBaldaCommand) error {
				t.Fatal("provider preflight called with invalid startup timeout")
				return nil
			}
			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), "balda.acp_startup_timeout") {
				t.Fatalf("command error = %v, want balda.acp_startup_timeout validation", err)
			}
			if _, err := os.Stat(filepath.Join(dir, "state")); !os.IsNotExist(err) {
				t.Fatalf("state directory exists after invalid configuration: %v", err)
			}
		})
	}
}

const acpTestRuntime = `runtime:
  providers:
    test-provider:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
`
