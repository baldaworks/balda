package main

import (
	"path/filepath"
	"testing"

	"github.com/normahq/runtime/v2/appconfig"
)

func TestLoadConfigDocumentAppliesMCPDeploymentKeyOverride(t *testing.T) {
	workingDir := t.TempDir()
	t.Setenv("BALDA_MCP_MANAGEMENT_CREDENTIAL_KEY", "deployment-key-fixture")
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), "runtime:\n  providers:\n    balda_agent:\n      type: opencode_acp\n      opencode_acp:\n        model: opencode/big-pickle\nbalda:\n  provider: balda_agent\n"); err != nil {
		t.Fatal(err)
	}
	var doc baldaTestConfigDocument
	_, err := appconfig.LoadConfigDocument(appconfig.RuntimeLoadOptions{WorkingDir: workingDir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true}, &doc)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Balda.MCPManagement.CredentialKey != "deployment-key-fixture" {
		t.Fatal("deployment credential key override was not applied")
	}
}
