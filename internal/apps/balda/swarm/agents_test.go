package swarm

import (
	"context"
	"testing"
)

type fakeQueueDepths map[string]int

func (f fakeQueueDepths) PendingCount(_ context.Context, mailbox string) (int, error) {
	return f[mailbox], nil
}

func TestNormalizeAgentSpecs_DefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	specs, err := NormalizeAgentSpecs(map[string]AgentSpec{
		AgentNameExecutor: {Role: "Execute with project tools", Tools: []string{AgentToolShell, AgentToolWorkspace, AgentToolShell}},
	})
	if err != nil {
		t.Fatalf("NormalizeAgentSpecs() error = %v", err)
	}
	byName := specsByName(specs)
	if _, ok := byName[AgentNamePlanner]; !ok {
		t.Fatalf("planner default missing: %+v", specs)
	}
	if got := byName[AgentNameExecutor].Role; got != "Execute with project tools" {
		t.Fatalf("executor role = %q, want override", got)
	}
	wantTools := []string{AgentToolWorkspace, AgentToolShell}
	if !equalStrings(byName[AgentNameExecutor].Tools, wantTools) {
		t.Fatalf("executor tools = %+v, want %+v", byName[AgentNameExecutor].Tools, wantTools)
	}
}

func TestNormalizeAgentSpecs_RejectsInvalidTool(t *testing.T) {
	t.Parallel()

	_, err := NormalizeAgentSpecs(map[string]AgentSpec{
		"custom": {Role: "Custom", Tools: []string{"root"}},
	})
	if err == nil {
		t.Fatal("NormalizeAgentSpecs() error = nil, want non-nil")
	}
}

func TestAgentAllocator_SelectsRoleAndTieBreaksByName(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRegistry(Config{})
	if err != nil {
		t.Fatalf("NewAgentRegistry() error = %v", err)
	}
	allocator := &AgentAllocator{registry: registry}
	got, err := allocator.Allocate(context.Background(), AgentAllocationRequest{Role: "validator"})
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if got.Name != AgentNameReviewer {
		t.Fatalf("allocated agent = %q, want %q", got.Name, AgentNameReviewer)
	}

	got, err = allocator.Allocate(context.Background(), AgentAllocationRequest{Tools: []string{AgentToolWorkspace}})
	if err != nil {
		t.Fatalf("Allocate(workspace) error = %v", err)
	}
	if got.Name != AgentNameExecutor {
		t.Fatalf("allocated agent = %q, want deterministic tie-break to %q", got.Name, AgentNameExecutor)
	}
}

func TestAgentAllocator_AppliesQueueDepthPenalty(t *testing.T) {
	t.Parallel()

	registry, err := NewAgentRegistry(Config{})
	if err != nil {
		t.Fatalf("NewAgentRegistry() error = %v", err)
	}
	allocator := &AgentAllocator{
		registry: registry,
		depths: fakeQueueDepths{
			"agent:" + AgentNameExecutor: 5,
			"agent:" + AgentNameReviewer: 0,
		},
	}
	got, err := allocator.Allocate(context.Background(), AgentAllocationRequest{Tools: []string{AgentToolWorkspace, AgentToolShell}})
	if err != nil {
		t.Fatalf("Allocate() error = %v", err)
	}
	if got.Name != AgentNameReviewer {
		t.Fatalf("allocated agent = %q, want %q", got.Name, AgentNameReviewer)
	}
}

func specsByName(specs []AgentSpec) map[string]AgentSpec {
	out := make(map[string]AgentSpec, len(specs))
	for _, spec := range specs {
		out[spec.Name] = spec
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
