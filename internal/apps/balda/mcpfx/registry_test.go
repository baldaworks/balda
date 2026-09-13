package mcpfx

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestRegistryProjectorUsesRevisionIdentityForNewRuntimes(t *testing.T) {
	t.Parallel()

	registry := mcpregistry.New(nil)
	projector, err := NewRegistryProjector(registry)
	if err != nil {
		t.Fatal(err)
	}
	key := mcpruntime.InstanceKey{
		Source:   runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"},
		Revision: "revision-one",
		Name:     "tools",
	}
	config := mcpruntime.LaunchConfig{
		Transport: "stdio", Command: "/managed/server", Args: []string{"serve"},
		Env: map[string]string{"TOKEN": "launch-only"}, WorkingDir: "/managed",
	}
	outcome, err := projector.Project(context.Background(), key, config)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != runtimecatalogcmd.MCPProjectionNewRuntimesOnly {
		t.Fatalf("outcome = %q, want new-runtimes-only", outcome)
	}
	stored, ok := registry.Get(RegistryID(key))
	if !ok || stored.Type != agentconfig.MCPServerTypeStdio || len(stored.Cmd) != 1 || stored.Cmd[0] != config.Command || stored.WorkingDir != config.WorkingDir {
		t.Fatalf("stored config = %+v, %v", stored, ok)
	}

	other := key
	other.Revision = "revision-two"
	if RegistryID(other) == RegistryID(key) {
		t.Fatal("revision-specific registry IDs collide")
	}
	if err := projector.Remove(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	if _, ok := registry.Get(RegistryID(key)); ok {
		t.Fatal("drained revision remains projected")
	}
}

func TestRegistryProjectorReportsUnsupportedTransport(t *testing.T) {
	t.Parallel()

	projector, err := NewRegistryProjector(mcpregistry.New(nil))
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := projector.Project(context.Background(), mcpruntime.InstanceKey{Name: "tools"}, mcpruntime.LaunchConfig{Transport: "unknown"})
	if err == nil || outcome != runtimecatalogcmd.MCPProjectionUnsupported {
		t.Fatalf("Project() = %q, %v; want explicit unsupported", outcome, err)
	}
}
