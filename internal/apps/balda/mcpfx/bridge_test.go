package mcpfx

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestBridgeLaunchKeepsUpstreamHeadersOutOfActualProviderRegistry(t *testing.T) {
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bridge.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindConfiguredMCP, Name: "worker"}, Revision: "exact-static-revision", Name: "worker"}
	original := mcpruntime.LaunchConfig{Transport: transportStreamableHTTP, URL: "https://upstream.example.org/mcp", Headers: map[string]string{"X-Worker-Secret": "resolved-private-header"}, EnforceHTTPOrigin: true}
	projected, err := BridgeLaunch(bridge, RegistryID(key), original, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	registry := mcpregistry.New(nil)
	projector, err := NewRegistryProjector(registry)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projector.Project(t.Context(), key, projected); err != nil {
		t.Fatal(err)
	}
	stored, ok := registry.Get(RegistryID(key))
	if !ok || !strings.HasPrefix(stored.URL, "http://127.0.0.1:") || len(stored.Headers) != 1 || stored.Headers[mcpbridge.CapabilityHeader] == "" || stored.Headers["X-Worker-Secret"] != "" {
		t.Fatal("provider registry received upstream credentials or an unusable direct transport")
	}
	if original.URL != "https://upstream.example.org/mcp" || original.Headers["X-Worker-Secret"] != "resolved-private-header" {
		t.Fatal("private projection mutated the original public launch definition")
	}
	changed := original
	changed.Headers = map[string]string{"X-Worker-Secret": "different-static-revision"}
	if _, err := BridgeLaunch(bridge, RegistryID(key), changed, nil, nil); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatal("changed static values silently replaced an exact retained revision")
	}
}

func TestBridgeLaunchFailsClosedWithoutReadinessAndPreservesDirectContracts(t *testing.T) {
	secured := mcpruntime.LaunchConfig{Transport: transportStreamableHTTP, URL: "https://upstream.example.org/mcp", Headers: map[string]string{"X-Worker-Secret": "resolved-private-header"}, EnforceHTTPOrigin: true}
	for _, bridge := range []*mcpbridge.Bridge{nil, mcpbridge.New(nil, nil)} {
		if _, err := BridgeLaunch(bridge, "secured", secured, nil, nil); !errors.Is(err, mcpcmd.ErrUnavailable) {
			t.Fatalf("credentials fell back to direct transport without bridge readiness: %v", err)
		}
		if bridge != nil {
			_ = bridge.Close(context.Background())
		}
	}
	for _, direct := range []mcpruntime.LaunchConfig{
		{Transport: "stdio", Command: "worker", Env: map[string]string{"WORKER_ENV": "stdio-private"}},
		{Transport: transportStreamableHTTP, URL: "https://upstream.example.org/mcp", EnforceHTTPOrigin: true},
		{Transport: "sse", URL: "https://upstream.example.org/sse"},
	} {
		projected, err := BridgeLaunch(nil, "direct", direct, nil, nil)
		if err != nil || projected.URL != direct.URL || projected.Command != direct.Command {
			t.Fatalf("supported direct transport was forced onto unavailable bridge: %v", err)
		}
		if _, err := providerConfig(projected); err != nil {
			t.Fatalf("supported direct provider config unavailable: %v", err)
		}
	}
}
