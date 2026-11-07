package managementhttp

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestConfigResolve(t *testing.T) {
	const token = "operator-token_123"
	for _, test := range []struct {
		name       string
		config     Config
		wantAddr   string
		wantErrKey string
	}{
		{name: "disabled", config: Config{}},
		{name: "IPv4 loopback", config: Config{Enabled: true, ListenAddr: "127.0.0.1:8096", OperatorToken: token}, wantAddr: "127.0.0.1:8096"},
		{name: "IPv6 loopback", config: Config{Enabled: true, ListenAddr: "[::1]:8096", OperatorToken: token}, wantAddr: "[::1]:8096"},
		{name: "missing address", config: Config{Enabled: true, OperatorToken: token}, wantErrKey: "listen_addr"},
		{name: "hostname", config: Config{Enabled: true, ListenAddr: "localhost:8096", OperatorToken: token}, wantErrKey: "loopback IP"},
		{name: "wildcard IPv4", config: Config{Enabled: true, ListenAddr: "0.0.0.0:8096", OperatorToken: token}, wantErrKey: "loopback IP"},
		{name: "wildcard IPv6", config: Config{Enabled: true, ListenAddr: "[::]:8096", OperatorToken: token}, wantErrKey: "loopback IP"},
		{name: "non-loopback", config: Config{Enabled: true, ListenAddr: "192.0.2.1:8096", OperatorToken: token}, wantErrKey: "loopback IP"},
		{name: "URL", config: Config{Enabled: true, ListenAddr: "http://127.0.0.1:8096", OperatorToken: token}, wantErrKey: "host:port"},
		{name: "zone", config: Config{Enabled: true, ListenAddr: "[::1%lo]:8096", OperatorToken: token}, wantErrKey: "loopback IP"},
		{name: "port zero", config: Config{Enabled: true, ListenAddr: "127.0.0.1:0", OperatorToken: token}, wantErrKey: "between 1 and 65535"},
		{name: "missing token", config: Config{Enabled: true, ListenAddr: "127.0.0.1:8096"}, wantErrKey: "operator_token is required"},
		{name: "whitespace token", config: Config{Enabled: true, ListenAddr: "127.0.0.1:8096", OperatorToken: "not valid"}, wantErrKey: "valid bearer token"},
		{name: "oversized token", config: Config{Enabled: true, ListenAddr: "127.0.0.1:8096", OperatorToken: strings.Repeat("a", maxOperatorTokenBytes+1)}, wantErrKey: "must not exceed"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolved, err := test.config.Resolve()
			if test.wantErrKey != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErrKey) {
					t.Fatalf("Resolve() error = %v, want key %q", err, test.wantErrKey)
				}
				if strings.Contains(fmt.Sprint(err), test.config.OperatorToken) && test.config.OperatorToken != "" {
					t.Fatal("Resolve() error exposed the operator token")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Enabled != test.config.Enabled || resolved.ListenAddr != test.wantAddr {
				t.Fatalf("Resolve() = %+v, want enabled=%v addr=%q", resolved, test.config.Enabled, test.wantAddr)
			}
			if strings.Contains(fmt.Sprintf("%+v", resolved), token) {
				t.Fatal("ResolvedConfig retained the plaintext operator token")
			}
		})
	}
}

func TestConfigResolveAllowsPortZeroOnlyForTests(t *testing.T) {
	config := Config{Enabled: true, ListenAddr: "127.0.0.1:0", OperatorToken: "operator-token"}
	resolved, err := config.resolve(true)
	if err != nil {
		t.Fatal(err)
	}
	if resolved.ListenAddr != "127.0.0.1:0" {
		t.Fatalf("ListenAddr = %q, want 127.0.0.1:0", resolved.ListenAddr)
	}
}

func TestConfigProjectionRedactsOperatorToken(t *testing.T) {
	const canary = "operator-token-canary"
	config := Config{Enabled: true, ListenAddr: "127.0.0.1:8096", OperatorToken: canary}
	jsonData, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	yamlData, err := yaml.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(jsonData), canary) || strings.Contains(string(yamlData), canary) {
		t.Fatal("serialized config exposed the operator token")
	}
}
