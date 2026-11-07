package main

import (
	"os"
	"path/filepath"
	"testing"

	baldaapp "github.com/baldaworks/balda/internal/apps/balda"
	"github.com/baldaworks/runtime/v2/appconfig"
)

func TestManagementOperatorTokenUsesStandardConfigPrecedence(t *testing.T) {
	for _, test := range []struct {
		name      string
		profile   string
		envToken  string
		sourceEnv string
		fileToken string
		wantToken string
	}{
		{name: "config", fileToken: "config-token", wantToken: "config-token"},
		{name: "profile", profile: "selected", fileToken: "config-token", wantToken: "profile-token"},
		{name: "environment overrides profile", profile: "selected", fileToken: "config-token", envToken: "environment-token", wantToken: "environment-token"},
		{name: "empty environment keeps profile", profile: "selected", fileToken: "config-token", wantToken: "profile-token"},
		{name: "YAML expansion", sourceEnv: "expanded-token", fileToken: "${MANAGEMENT_TOKEN_SOURCE}", wantToken: "expanded-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BALDA_MANAGEMENT_OPERATOR_TOKEN", test.envToken)
			t.Setenv("MANAGEMENT_TOKEN_SOURCE", test.sourceEnv)
			dir := t.TempDir()
			content := []byte("runtime:\n  providers:\n    agent:\n      type: opencode_acp\n      opencode_acp:\n        model: test-model\nbalda:\n  management:\n    enabled: true\n    listen_addr: 127.0.0.1:8096\n    operator_token: " + test.fileToken + "\nprofiles:\n  selected:\n    balda:\n      management:\n        operator_token: profile-token\n")
			if err := os.WriteFile(filepath.Join(dir, "config.yaml"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			var document struct {
				Balda baldaapp.BaldaConfig `mapstructure:"balda"`
			}
			_, err := appconfig.LoadConfigDocument(
				appconfig.RuntimeLoadOptions{WorkingDir: dir, ConfigDir: dir, Profile: test.profile},
				appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
				&document,
			)
			if err != nil {
				t.Fatal(err)
			}
			if document.Balda.Management.OperatorToken != test.wantToken {
				t.Fatal("operator token did not follow the expected config precedence")
			}
		})
	}
}

func TestManagementEnvironmentCanPopulateKnownDefaultKeys(t *testing.T) {
	t.Setenv("BALDA_MANAGEMENT_ENABLED", "true")
	t.Setenv("BALDA_MANAGEMENT_LISTEN_ADDR", "127.0.0.1:8096")
	t.Setenv("BALDA_MANAGEMENT_OPERATOR_TOKEN", "environment-token")
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("runtime:\n  providers:\n    agent:\n      type: opencode_acp\n      opencode_acp:\n        model: test-model\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	var document struct {
		Balda baldaapp.BaldaConfig `mapstructure:"balda"`
	}
	_, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{WorkingDir: dir, ConfigDir: dir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
		&document,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := document.Balda.ResolveManagement()
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Enabled || resolved.ListenAddr != "127.0.0.1:8096" {
		t.Fatalf("resolved management = %+v", resolved)
	}
}
