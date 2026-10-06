package catalogapp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestCurrentStartupAuthorizationReadiness(t *testing.T) {
	for _, transport := range []agentconfig.MCPServerType{agentconfig.MCPServerTypeHTTP, agentconfig.MCPServerTypeSSE} {
		t.Run(string(transport), func(t *testing.T) {
			for _, mixed := range []bool{false, true} {
				t.Run(fmt.Sprint(mixed), func(t *testing.T) {
					p, original, _, _, credentials := hybridCatalogFixture(t)
					var origin string
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
						if request.URL.Path == "/fatal" {
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						w.Header().Set("WWW-Authenticate", fmt.Sprintf(`Bearer resource_metadata="%s/.well-known/oauth-protected-resource"`, origin))
						w.WriteHeader(http.StatusUnauthorized)
					}))
					defer upstream.Close()
					origin = upstream.URL
					configured := map[string]agentconfig.MCPServerConfig{"auth": {Type: transport, URL: origin + "/auth", Headers: map[string]string{"X-Worker": "private configured value"}}}
					if mixed {
						configured["fatal"] = agentconfig.MCPServerConfig{Type: transport, URL: origin + "/fatal"}
					}
					bridge := mcpbridge.New(nil, nil)
					if err := bridge.Start(t.Context()); err != nil {
						t.Fatal(err)
					}
					defer func() { _ = bridge.Close(context.Background()) }()
					r, err := NewRuntime(original.stateDir, "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, bridge)
					if err != nil {
						t.Fatal(err)
					}
					defer func() { _ = r.MCP().Shutdown(context.Background()) }()
					selected := []string{"auth"}
					if mixed {
						selected = append(selected, "fatal")
					}
					if err := r.configureProviderMCP(map[string]agentconfig.Config{"alpha": {MCPServers: selected}}, "alpha", nil); err != nil {
						t.Fatal(err)
					}
					snapshot, err := r.PreparePluginCandidate(t.Context(), nil)
					if err != nil {
						t.Fatal(err)
					}
					if err := r.PublishCandidate(t.Context(), snapshot); err != nil {
						t.Fatal(err)
					}
					_, lease, err := r.AcquireProviderMCPServerIDs(t.Context(), snapshot.ID, map[string][]string{"alpha": selected})
					if lease != nil || err == nil {
						t.Fatal("startup returned incomplete execution capabilities")
					}
					if got := r.MCPAuthorizationPending(t.Context(), fmt.Errorf("bind provider capabilities: %w", err)); got == mixed {
						t.Fatalf("pending = %t, want %t; blockers: %v", got, !mixed, err)
					}
					for _, name := range selected {
						item := mcpcmd.Item{Connection: mcpcmd.Connection{ID: "config:" + name, PublicID: name, Source: mcpcmd.SourceConfig, Enabled: true}}
						recovery, readErr := r.CurrentMCPRecovery(t.Context(), item)
						want := mcpcmd.RecoveryReason("")
						if name == "auth" {
							want = mcpcmd.RecoveryFirstAuthorization
						}
						if readErr != nil || recovery != want {
							t.Fatalf("current recovery %s = %s/%v, want %s", name, recovery, readErr, want)
						}
						item.Status = mcpcmd.StatusReady
						if recovery, _ := r.CurrentMCPRecovery(t.Context(), item); recovery != "" {
							t.Fatal("ready inventory offered recovery")
						}
						item.Status = mcpcmd.StatusUnavailable
						item.Connection.Enabled = false
						if recovery, _ := r.CurrentMCPRecovery(t.Context(), item); recovery != "" {
							t.Fatal("disabled inventory offered recovery")
						}
						canceled, cancel := context.WithCancel(t.Context())
						cancel()
						if recovery, readErr := r.CurrentMCPRecovery(canceled, item); recovery != "" || !errors.Is(readErr, context.Canceled) {
							t.Fatal("cancellation offered recovery")
						}
					}
					var exact *mcpruntime.AttachmentError
					if !errors.As(err, &exact) {
						t.Fatal("startup lost exact bounded MCP failure")
					}
					unknown := *exact
					unknown.Key.Revision += "-missing"
					if r.MCPAuthorizationPending(t.Context(), &unknown) {
						t.Fatal("unknown historical revision became recoverable current startup")
					}
					canceled, cancel := context.WithCancel(t.Context())
					cancel()
					if r.MCPAuthorizationPending(canceled, err) {
						t.Fatal("cancellation admitted unavailable startup")
					}
				})
			}
		})
	}
}
