package catalogapp

import (
	"context"
	"errors"
	"maps"
	"slices"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	"github.com/baldaworks/runtime/v2/mcpregistry"
)

type nativePreparerFixture struct {
	mu       sync.Mutex
	requests []hooknative.LeafRequest
	released []string
	failLeaf string
	required bool
	mutate   bool
}

func (p *nativePreparerFixture) Required(context.Context, string) (bool, error) {
	return p.required, nil
}

func (p *nativePreparerFixture) Prepare(_ context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	if p.mutate {
		request.Config.MCPServers[0] = "mutated-mcp"
		request.Config.CodexACP.Cmd[0] = "mutated-command"
		request.Config.CodexACP.ExtraArgs[0] = "mutated-argument"
	}
	p.mu.Lock()
	p.requests = append(p.requests, request)
	p.mu.Unlock()
	if request.LeafID == p.failLeaf {
		return hooknative.PreparedLeaf{}, errors.New("fixture preparation failed")
	}
	config := request.Config
	config.Type = agentconfig.AgentTypeGenericACP
	config.GenericACP = &agentconfig.ACPConfig{Cmd: []string{"prepared", request.LeafID}}
	return hooknative.PreparedLeaf{Config: config, Release: func() error {
		p.mu.Lock()
		defer p.mu.Unlock()
		p.released = append(p.released, request.LeafID)
		return nil
	}}, nil
}

func TestProviderFactoryPreparesNestedACPLeavesAndBypassesHostedLeaves(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"root":   poolConfig("nested", "hosted"),
		"nested": poolConfig("first", "second", "first"),
		"first":  acpConfig("first"),
		"second": acpConfig("second"),
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{}},
	}
	preparer := &nativePreparerFixture{required: true}
	factory := NewProviderFactory(providers, mcpregistry.New(nil))
	factory.ConfigureNativeHooks(preparer)
	prepared, release, err := factory.prepareNativeHooks(t.Context(), agentfactory.BuildRequest{AgentID: "root", WorkingDirectory: t.TempDir()}, "session")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := leafIDs(preparer.requests), []string{"first", "second"}; !slices.Equal(got, want) {
		t.Fatalf("prepared leaves = %v, want %v", got, want)
	}
	if prepared["first"].Type != agentconfig.AgentTypeGenericACP || prepared["second"].Type != agentconfig.AgentTypeGenericACP {
		t.Fatal("ACP leaves were not replaced")
	}
	if prepared["hosted"].Type != agentconfig.AgentTypeOpenAI {
		t.Fatal("hosted leaf was changed")
	}
	if providers["first"].Type != agentconfig.AgentTypeCodexACP || factory.providers["first"].Type != agentconfig.AgentTypeCodexACP {
		t.Fatal("caller-owned provider maps were mutated")
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if got, want := preparer.released, []string{"second", "first"}; !slices.Equal(got, want) {
		t.Fatalf("release order = %v, want %v", got, want)
	}
}

func TestProviderFactoryReleasesPreparedLeavesOnPartialFailure(t *testing.T) {
	providers := map[string]agentconfig.Config{
		"root":   poolConfig("first", "second"),
		"first":  acpConfig("first"),
		"second": acpConfig("second"),
	}
	preparer := &nativePreparerFixture{required: true, failLeaf: "second"}
	factory := NewProviderFactory(providers, mcpregistry.New(nil))
	factory.ConfigureNativeHooks(preparer)
	if _, _, err := factory.prepareNativeHooks(t.Context(), agentfactory.BuildRequest{AgentID: "root", WorkingDirectory: t.TempDir()}, "session"); err == nil {
		t.Fatal("partial preparation succeeded")
	}
	if got, want := preparer.released, []string{"first"}; !slices.Equal(got, want) {
		t.Fatalf("released leaves = %v, want %v", got, want)
	}
}

func TestProviderFactoryRejectsInvalidNativeProviderTrees(t *testing.T) {
	tests := []struct {
		name      string
		providers map[string]agentconfig.Config
	}{
		{name: "missing member", providers: map[string]agentconfig.Config{"root": poolConfig("missing")}},
		{name: "cycle", providers: map[string]agentconfig.Config{"root": poolConfig("nested"), "nested": poolConfig("root")}},
		{name: "empty pool", providers: map[string]agentconfig.Config{"root": poolConfig()}},
		{name: "empty member", providers: map[string]agentconfig.Config{"root": poolConfig(" ")}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			factory := NewProviderFactory(test.providers, mcpregistry.New(nil))
			factory.ConfigureNativeHooks(&nativePreparerFixture{required: true})
			_, _, err := factory.prepareNativeHooks(t.Context(), agentfactory.BuildRequest{AgentID: "root", WorkingDirectory: t.TempDir()}, "session")
			if !errors.Is(err, hookcmd.ErrUnsupported) {
				t.Fatalf("prepareNativeHooks() error = %v, want unsupported", err)
			}
		})
	}
}

func TestProviderFactoryNativePreparationIsConcurrentAndImmutable(t *testing.T) {
	providers := map[string]agentconfig.Config{"provider": acpConfig("provider")}
	preparer := &nativePreparerFixture{required: true, mutate: true}
	factory := NewProviderFactory(providers, mcpregistry.New(nil))
	factory.ConfigureNativeHooks(preparer)
	const count = 16
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			prepared, release, err := factory.prepareNativeHooks(t.Context(), agentfactory.BuildRequest{AgentID: "provider", WorkingDirectory: t.TempDir()}, "session")
			if err != nil {
				t.Error(err)
				return
			}
			if prepared["provider"].Type != agentconfig.AgentTypeGenericACP {
				t.Error("provider was not prepared")
			}
			if err := release(); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if !maps.EqualFunc(providers, factory.providers, equalNestedProviderConfig) {
		t.Fatal("shared provider map changed during concurrent preparation")
	}
	if got := providers["provider"].CodexACP.Cmd[0]; got != "provider" {
		t.Fatalf("caller command = %q, want provider", got)
	}
	if got := providers["provider"].CodexACP.ExtraArgs[0]; got != "original-argument" {
		t.Fatalf("caller argument = %q, want original-argument", got)
	}
	if got := providers["provider"].MCPServers[0]; got != "original-mcp" {
		t.Fatalf("caller MCP = %q, want original-mcp", got)
	}
}

func poolConfig(members ...string) agentconfig.Config {
	return agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: members}}
}

func acpConfig(name string) agentconfig.Config {
	return agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, MCPServers: []string{"original-mcp"}, CodexACP: &agentconfig.ACPConfig{Cmd: []string{name}, ExtraArgs: []string{"original-argument"}}}
}

func equalNestedProviderConfig(a, b agentconfig.Config) bool {
	return a.Type == b.Type &&
		slices.Equal(a.MCPServers, b.MCPServers) &&
		a.CodexACP != nil && b.CodexACP != nil &&
		slices.Equal(a.CodexACP.Cmd, b.CodexACP.Cmd) &&
		slices.Equal(a.CodexACP.ExtraArgs, b.CodexACP.ExtraArgs)
}

func leafIDs(requests []hooknative.LeafRequest) []string {
	ids := make([]string, 0, len(requests))
	for _, request := range requests {
		ids = append(ids, request.LeafID)
	}
	return ids
}
