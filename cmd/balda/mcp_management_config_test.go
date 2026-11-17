package main

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	baldaapp "github.com/baldaworks/balda/internal/apps/balda"
	"github.com/spf13/viper"
)

const testApplicationSecurityKey = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

func TestLoadConfigDocumentAppliesApplicationSecurityKeyOverride(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("BALDA_APP_SECURITY_KEY", testApplicationSecurityKey)
	viper.Reset()
	t.Cleanup(viper.Reset)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), "runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n"); err != nil {
		t.Fatal(err)
	}
	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.doc.App.SecurityKey != testApplicationSecurityKey || prepared.baldaCfg.App.SecurityKey != testApplicationSecurityKey {
		t.Fatal("application security key override was not applied")
	}
}

func TestLoadConfigDocumentAppliesApplicationSecurityKeyFromYAML(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	viper.Reset()
	t.Cleanup(viper.Reset)
	config := fmt.Sprintf("app:\n  security_key: %q\nruntime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n", testApplicationSecurityKey)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), config); err != nil {
		t.Fatal(err)
	}

	prepared, err := loadBaldaCommandConfig(false)
	if err != nil {
		t.Fatal(err)
	}
	if prepared.baldaCfg.App.SecurityKey != testApplicationSecurityKey {
		t.Fatal("app.security_key YAML value was not applied")
	}
}

func TestLoadBaldaCommandConfigRejectsInvalidApplicationSecurityKey(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("BALDA_APP_SECURITY_KEY", "not-base64")
	viper.Reset()
	t.Cleanup(viper.Reset)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), "runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n"); err != nil {
		t.Fatal(err)
	}

	_, err := loadBaldaCommandConfig(false)
	if err == nil || !strings.Contains(err.Error(), "app.security_key is invalid") || strings.Contains(err.Error(), "not-base64") {
		t.Fatalf("loadBaldaCommandConfig() error = %v", err)
	}
}

func TestLoadBaldaCommandConfigRejectsObsoleteEncryptionRoot(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	t.Setenv("BALDA_MCP_MANAGEMENT_CREDENTIAL_KEY", "obsolete-key")
	viper.Reset()
	t.Cleanup(viper.Reset)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), "runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n"); err != nil {
		t.Fatal(err)
	}

	_, err := loadBaldaCommandConfig(false)
	if err == nil || !strings.Contains(err.Error(), "move the identical value to app.security_key") {
		t.Fatalf("loadBaldaCommandConfig() error = %v", err)
	}
}

func TestResolveSecurityKeyRejectsConflictingRoots(t *testing.T) {
	cfg := baldaapp.Config{
		App: baldaapp.AppConfig{SecurityKey: "canonical"},
		Balda: baldaapp.BaldaConfig{MCPManagement: baldaapp.MCPManagementConfig{
			CredentialKey: "obsolete",
		}},
	}
	if _, err := cfg.ResolveSecurityKey(); err == nil {
		t.Fatal("ResolveSecurityKey() accepted two configured roots")
	}
}

func TestResolveSecurityKeyAcceptsMissingAndValidCanonicalRoot(t *testing.T) {
	for _, want := range []string{"", testApplicationSecurityKey} {
		cfg := baldaapp.Config{App: baldaapp.AppConfig{SecurityKey: want}}
		got, err := cfg.ResolveSecurityKey()
		if err != nil || got != want {
			t.Fatalf("ResolveSecurityKey() = %q, %v, want %q", got, err, want)
		}
	}
}
