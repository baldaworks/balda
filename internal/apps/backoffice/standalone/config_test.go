package standalone

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/runtime/v2/appconfig"
	"github.com/stretchr/testify/require"
)

func TestResolveConfig(t *testing.T) {
	basePath := "/balda"
	resolved, err := (Config{
		HTTP:       HTTPConfig{BaseURL: "https://admin.example.test", BasePath: &basePath},
		Backoffice: BackofficeConfig{ListenAddr: "0.0.0.0:8096"},
		Management: ManagementConfig{Enabled: true, ListenAddr: "127.0.0.1:8097", OperatorToken: "operator-token"},
	}).Resolve()
	require.NoError(t, err)
	require.Equal(t, "0.0.0.0:8096", resolved.ListenAddr)
	require.Equal(t, "https://admin.example.test", resolved.PublicOrigin)
	require.Equal(t, "/balda/backoffice", resolved.MountPath)
	require.Equal(t, "http://127.0.0.1:8097", resolved.CoreURL)
	require.Equal(t, "operator-token", resolved.OperatorToken)
}

func TestResolveConfigRejectsUnsafeInputs(t *testing.T) {
	valid := Config{
		HTTP:       HTTPConfig{BaseURL: "http://127.0.0.1:8095"},
		Backoffice: BackofficeConfig{ListenAddr: "127.0.0.1:8096"},
		Management: ManagementConfig{Enabled: true, ListenAddr: "127.0.0.1:8097", OperatorToken: "operator-token"},
	}
	for _, test := range []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "public HTTP bind", mutate: func(c *Config) { c.Backoffice.ListenAddr = "0.0.0.0:8096" }},
		{name: "public URL path", mutate: func(c *Config) { c.HTTP.BaseURL = "https://example.test/backoffice" }},
		{name: "noncanonical base path", mutate: func(c *Config) { value := "/balda/"; c.HTTP.BasePath = &value }},
		{name: "management disabled", mutate: func(c *Config) { c.Management.Enabled = false }},
		{name: "management hostname", mutate: func(c *Config) { c.Management.ListenAddr = "localhost:8097" }},
		{name: "management public", mutate: func(c *Config) { c.Management.ListenAddr = "192.0.2.2:8097" }},
		{name: "missing token", mutate: func(c *Config) { c.Management.OperatorToken = "" }},
		{name: "invalid token", mutate: func(c *Config) { c.Management.OperatorToken = "token with spaces" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			config := valid
			test.mutate(&config)
			_, err := config.Resolve()
			require.Error(t, err)
		})
	}
}

func TestLoadConfigDocumentUsesStandardPrecedence(t *testing.T) {
	const defaults = `runtime: {}
balda:
  http:
    base_url: http://127.0.0.1:8095
    base_path: ""
  backoffice:
    listen_addr: 127.0.0.1:8096
  management:
    enabled: true
    listen_addr: 127.0.0.1:8097
    operator_token: default-token
`
	for _, test := range []struct {
		name, profile, envToken, sourceToken, fileToken, want string
	}{
		{name: "config", fileToken: "config-token", want: "config-token"},
		{name: "profile", profile: "selected", fileToken: "config-token", want: "profile-token"},
		{name: "environment", profile: "selected", envToken: "environment-token", fileToken: "config-token", want: "environment-token"},
		{name: "empty environment", profile: "selected", fileToken: "config-token", want: "profile-token"},
		{name: "interpolation", sourceToken: "expanded-token", fileToken: "${OPERATOR_SOURCE}", want: "expanded-token"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("BALDA_MANAGEMENT_OPERATOR_TOKEN", test.envToken)
			t.Setenv("OPERATOR_SOURCE", test.sourceToken)
			directory := t.TempDir()
			content := "runtime:\n  providers:\n    agent:\n      type: opencode_acp\n      opencode_acp:\n        model: test-model\nbalda:\n  management:\n    operator_token: " + test.fileToken + "\nprofiles:\n  selected:\n    balda:\n      management:\n        operator_token: profile-token\n"
			require.NoError(t, os.WriteFile(filepath.Join(directory, "config.yaml"), []byte(content), 0o600))
			document, selected, err := LoadConfigDocument(appconfig.RuntimeLoadOptions{
				WorkingDir: directory, ConfigDir: directory, Profile: test.profile,
			}, []byte(defaults))
			require.NoError(t, err)
			wantProfile := test.profile
			if wantProfile == "" {
				wantProfile = "default"
			}
			require.Equal(t, wantProfile, selected)
			require.Equal(t, test.want, document.Balda.Management.OperatorToken)
		})
	}
}

func TestConfigSerializationRedactsOperatorToken(t *testing.T) {
	document := Document{Balda: Config{Management: ManagementConfig{OperatorToken: "credential-canary"}}}
	encoded, err := json.Marshal(document)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "credential-canary")
	require.NotContains(t, strings.ToLower(string(encoded)), "operator_token")
}
