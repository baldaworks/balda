package catalogapp

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpfx"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestConfiguredCaptureTransitionsKeepRetainedIdentity(t *testing.T) {
	const wrongKey = "wrong key"
	const remote = "remote"
	p, original, _, mutation, credentials := hybridCatalogFixture(t)
	configured := map[string]agentconfig.MCPServerConfig{"worker-tools": {Type: agentconfig.MCPServerTypeHTTP, URL: "https://worker.example/mcp", Headers: map[string]string{"X-Worker": "original-private-value"}}}
	providers := map[string]agentconfig.Config{"alpha": {MCPServers: []string{"worker-tools"}}}
	catalog, err := NewRuntime(original.stateDir, "", "", p, nil, configured, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = catalog.MCP().Shutdown(t.Context()) }()
	probe, err := mcpfx.NewManagedProbe(credentials, mcpfx.NewClientLauncher(), nil)
	if err != nil {
		t.Fatal(err)
	}
	definitions, err := mcpmanage.NewDefinitions(credentials, mcpfx.NewDefinitionStore(p.MCP()), mcpfx.NewConfiguredDefinitions(configured, providers, "alpha", nil), catalog, probe)
	if err != nil {
		t.Fatal(err)
	}
	capture, err := definitions.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:worker-tools", Authority: mutation.Authority})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []string{remote, transportStdio, "removed", wrongKey} {
		t.Run(change, func(t *testing.T) {
			current := map[string]agentconfig.MCPServerConfig{}
			values := credentials
			switch change {
			case remote, wrongKey:
				current["worker-tools"] = agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: "https://worker.example/changed", Headers: map[string]string{"X-Worker": "changed-private-value"}}
			case transportStdio:
				current["worker-tools"] = agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeStdio, Cmd: []string{"fixture-current-stdio"}}
			}
			if change == wrongKey {
				key := make([]byte, 32)
				key[0] = 1
				values, err = mcpmanage.New(base64.StdEncoding.EncodeToString(key))
				if err != nil {
					t.Fatal(err)
				}
			}
			bridge := mcpbridge.New(nil, nil)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() { _ = bridge.Close(context.Background()) }()
			restarted, err := NewRuntime(original.stateDir, "", "", p, nil, current, mcpregistry.New(nil), commandcmd.NewRegistry(), values, bridge)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = restarted.MCP().Shutdown(t.Context()) }()
			snapshot, err := restarted.PreparePluginCandidate(t.Context(), nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if change == "removed" {
				if len(snapshot.MCPServers) != 0 {
					t.Fatal("removed declaration remains selected")
				}
			} else {
				for _, descriptor := range snapshot.MCPServers {
					if change == transportStdio {
						if descriptor.ConfigRef != "" || descriptor.Transport != transportStdio {
							t.Fatalf("current stdio borrowed remote capture: %+v", descriptor)
						}
						if err := restarted.PublishCandidate(t.Context(), snapshot); err != nil {
							t.Fatal(err)
						}
						connection, found, err := p.MCP().GetMCPConnection(t.Context(), capture.ConnectionID)
						if err != nil || !found {
							t.Fatal("retained configured identity missing")
						}
						status, _, err := restarted.MCPHealth(t.Context(), connection)
						if err != nil || status != mcpcmd.StatusUnavailable {
							t.Fatalf("current stdio launch failure = %s/%v, want unavailable without remote authorization", status, err)
						}
					} else {
						if err := restarted.PublishCandidate(t.Context(), snapshot); err != nil {
							t.Fatal(err)
						}
						keys, lease, err := restarted.MCP().AcquireDescriptors(t.Context(), []runtimecatalogcmd.MCPServerDescriptor{descriptor})
						var failure *mcpruntime.AttachmentError
						if change == wrongKey {
							if !errors.As(err, &failure) || failure.Reason != mcpruntime.FailureUnavailable || lease != nil {
								t.Fatalf("missing protection material = %v", err)
							}
						} else {
							if err != nil || len(keys) != 1 || lease == nil {
								t.Fatalf("recapture-required configuration lease = %v", err)
							}
							lease()
						}
					}
				}
			}
			connection, found, err := p.MCP().GetMCPConnection(t.Context(), capture.ConnectionID)
			if err != nil || !found {
				t.Fatal("retained capture connection missing")
			}
			recovery, err := restarted.CurrentMCPRecovery(t.Context(), mcpcmd.Item{Connection: connection})
			wantRecovery := mcpcmd.RecoveryReason("")
			if change == remote {
				wantRecovery = mcpcmd.RecoveryCaptureRequired
			}
			if err != nil || recovery != wantRecovery {
				t.Fatalf("current recovery = %s/%v, want %s", recovery, err, wantRecovery)
			}
			stale := connection
			stale.CurrentRevisionID += "-historical"
			if recovery, _ := restarted.CurrentMCPRecovery(t.Context(), mcpcmd.Item{Connection: stale}); recovery != "" {
				t.Fatal("historical capture offered current recovery")
			}
			retained, found, err := p.MCP().GetMCPRevision(t.Context(), capture.ConnectionID, capture.ID)
			if err != nil || !found || retained.ID != capture.ID || retained.Definition.URL != capture.Definition.URL {
				t.Fatal("file transition lost the exact retained capture")
			}
		})
	}
}
