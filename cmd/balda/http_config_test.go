package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/normahq/runtime/v2/appconfig"
)

func TestLoadConfigDocumentAppliesSharedHTTPEnvOverrides(t *testing.T) {
	workingDir := t.TempDir()
	t.Setenv("BALDA_HTTP_LISTEN_ADDR", "127.0.0.1:18095")
	t.Setenv("BALDA_HTTP_BASE_URL", "https://lab.metalagman.dev")
	t.Setenv("BALDA_HTTP_BASE_PATH", "/balda")
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  backoffice:
    base_path: /legacy
`); err != nil {
		t.Fatal(err)
	}
	var doc baldaConfigDocument
	_, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{WorkingDir: workingDir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
		&doc,
	)
	if err != nil {
		t.Fatal(err)
	}
	applyHTTPBasePathEnv(&doc.Balda)
	resolved, err := doc.Balda.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ListenAddr != "127.0.0.1:18095" || resolved.BaseURL != "https://lab.metalagman.dev" || resolved.BasePath != "/balda" {
		t.Fatalf("HTTP env overrides = %+v", resolved)
	}
}

func TestApplyHTTPBasePathEnvCanSelectRoot(t *testing.T) {
	t.Setenv("BALDA_HTTP_BASE_PATH", "")
	var cfg baldaConfigDocument
	legacy := "/legacy"
	cfg.Balda.Backoffice.BasePath = legacy
	applyHTTPBasePathEnv(&cfg.Balda)
	resolved, err := cfg.Balda.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BasePath != "" {
		t.Fatalf("base path = %q, want root", resolved.BasePath)
	}
}

func TestLoadConfigDocumentPreservesLegacyHTTPFallback(t *testing.T) {
	workingDir := t.TempDir()
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  backoffice:
    listen_addr: "127.0.0.1:19095"
    public_url: "https://old.example.test"
    base_path: /old
`); err != nil {
		t.Fatal(err)
	}
	var doc baldaConfigDocument
	_, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{WorkingDir: workingDir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
		&doc,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := doc.Balda.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ListenAddr != "127.0.0.1:19095" || resolved.BaseURL != "https://old.example.test" || resolved.BasePath != "/old" {
		t.Fatalf("legacy HTTP fallback = %+v", resolved)
	}
}

func TestLoadConfigDocumentCanOverrideLegacyPrefixWithRoot(t *testing.T) {
	workingDir := t.TempDir()
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  backoffice:
    base_path: /legacy
  http:
    base_path: ""
`); err != nil {
		t.Fatal(err)
	}
	var doc baldaConfigDocument
	_, err := appconfig.LoadConfigDocument(
		appconfig.RuntimeLoadOptions{WorkingDir: workingDir},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true},
		&doc,
	)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := doc.Balda.ResolveHTTP()
	if err != nil {
		t.Fatal(err)
	}
	if resolved.BasePath != "" {
		t.Fatalf("base path = %q, want explicit root", resolved.BasePath)
	}
}

func TestLoadBaldaCommandConfigRejectsInvalidSharedHTTP(t *testing.T) {
	for _, tt := range []struct {
		name, setting, field string
	}{
		{name: "origin path", setting: "base_url: https://lab.metalagman.dev/extra", field: "balda.http.base_url"},
		{name: "relative prefix", setting: "base_path: balda", field: "balda.http.base_path"},
		{name: "invalid bind", setting: "listen_addr: 127.0.0.1:invalid", field: "balda.http.listen_addr"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			workingDir := t.TempDir()
			content := `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  http:
    ` + tt.setting + "\n"
			if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), content); err != nil {
				t.Fatal(err)
			}
			originalWD, err := os.Getwd()
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chdir(workingDir); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chdir(originalWD) })
			_, err = loadBaldaCommandConfig(false)
			if err == nil || !strings.Contains(err.Error(), tt.field) {
				t.Fatalf("loadBaldaCommandConfig() error = %v, want %s", err, tt.field)
			}
		})
	}
}

func TestLoadedDefaultsDoNotAdvertiseWebhookPublicOrigin(t *testing.T) {
	workingDir := t.TempDir()
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
`); err != nil {
		t.Fatal(err)
	}
	var doc baldaConfigDocument
	_, err := appconfig.LoadConfigDocument(appconfig.RuntimeLoadOptions{WorkingDir: workingDir}, appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaultBaldaConfig, UseDotConfigAppDir: true}, &doc)
	if err != nil {
		t.Fatal(err)
	}
	server, err := doc.Balda.ResolveSharedBackofficeServer()
	if err != nil {
		t.Fatal(err)
	}
	if server.WebhookPublicOrigin == nil || *server.WebhookPublicOrigin != "" {
		t.Fatalf("default public webhook origin = %v, want explicit empty", server.WebhookPublicOrigin)
	}
	if server.PublicURL == "" {
		t.Fatal("missing browser security origin")
	}
}
