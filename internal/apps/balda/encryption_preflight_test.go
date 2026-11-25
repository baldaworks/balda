package balda

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
)

func TestPreflightValidatesRetainedEncryptedStateBeforeRuntimeStartup(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x31}, 32))
	wrongKey := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0x32}, 32))
	for _, tc := range []struct {
		name             string
		seedMCP          bool
		seedProvider     bool
		preflightKey     string
		wantError        error
		wantErrorContext string
	}{
		{name: "matching mixed state", seedMCP: true, seedProvider: true, preflightKey: key},
		{name: "missing MCP root", seedMCP: true, wantError: mcpcmd.ErrCredentials, wantErrorContext: "retained MCP"},
		{name: "wrong MCP root", seedMCP: true, preflightKey: wrongKey, wantError: mcpcmd.ErrCredentials, wantErrorContext: "retained MCP"},
		{name: "missing Provider root", seedProvider: true, wantError: providers.ErrUnavailable, wantErrorContext: "retained provider"},
		{name: "wrong Provider root", seedProvider: true, preflightKey: wrongKey, wantError: providers.ErrUnavailable, wantErrorContext: "retained provider"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, norma, trace := acpWiringConfig(t, "300ms", "30s")
			cfg.Balda.Security.Key = tc.preflightKey
			if err := os.MkdirAll(cfg.Balda.StateDir, 0o700); err != nil {
				t.Fatal(err)
			}
			provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(cfg.Balda.StateDir, "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			if tc.seedMCP {
				mcp, err := newMCPService(key)
				if err != nil {
					t.Fatal(err)
				}
				persistStartupMCPRevisions(t, provider, mcp)
			}
			if tc.seedProvider {
				persistStartupProviderRevision(t, provider, key)
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}

			err = PreflightRuntime(t.Context(), cfg, norma, runtimeconfig.RuntimeLoadOptions{})
			if tc.wantError == nil {
				if err != nil {
					t.Fatalf("matching retained state failed preflight: %v", err)
				}
			} else if !errors.Is(err, tc.wantError) || !strings.Contains(err.Error(), tc.wantErrorContext) {
				t.Fatalf("preflight error = %v, want bounded %v with %q context", err, tc.wantError, tc.wantErrorContext)
			}
			if err != nil && strings.Contains(err.Error(), "startup-provider-secret") {
				t.Fatal("preflight error exposed protected value")
			}
			if _, statErr := os.Stat(trace); !os.IsNotExist(statErr) {
				t.Fatalf("preflight launched selected provider before retained-state validation: %v", statErr)
			}
		})
	}
}

func persistStartupProviderRevision(t *testing.T, provider state.Provider, key string) {
	t.Helper()
	root, err := secrets.NewRoot(key)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := root.Protector(secrets.ScopeProviderRevision)
	if err != nil {
		t.Fatal(err)
	}
	apiKey := "startup-provider-secret"
	now := time.Now().UTC()
	service := providers.New(nil, provider.ProviderManagement(), protector)
	_, err = service.Create(t.Context(), providers.Change{ID: "startup-provider",
		Config:    agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: "startup-model"}},
		Protected: providers.ProtectedEdit{APIKey: &apiKey},
		Audit:     usercmd.AuditEvent{ID: "startup-provider-create", Source: "encryption-preflight", OccurredAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
}
