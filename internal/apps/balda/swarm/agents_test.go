package swarm

import (
	"context"
	"testing"
)

func TestNormalizeAgentSpecs_DefaultsAndOverrides(t *testing.T) {
	t.Parallel()

	specs, err := NormalizeAgentSpecs(map[string]AgentSpec{
		AgentNameExecutor: {Role: "Execute with project tools", Tools: []string{AgentToolShell, AgentToolWorkspace, AgentToolShell}},
	})
	if err != nil {
		t.Fatalf("NormalizeAgentSpecs() error = %v", err)
	}
	byName := specsByName(specs)
	planner, ok := byName[AgentNamePlanner]
	if !ok {
		t.Fatalf("planner default missing: %+v", specs)
	}
	wantPlannerTools := []string{AgentToolWorkspace, AgentToolShell, AgentToolMCP}
	if !equalStrings(planner.Tools, wantPlannerTools) {
		t.Fatalf("planner tools = %+v, want %+v", planner.Tools, wantPlannerTools)
	}
	executor, ok := byName[AgentNameExecutor]
	if !ok {
		t.Fatalf("executor missing after override merge: %+v", specs)
	}
	if got := executor.Role; got != "Execute with project tools" {
		t.Fatalf("executor role = %q, want override", got)
	}
	wantTools := []string{AgentToolWorkspace, AgentToolShell}
	if !equalStrings(executor.Tools, wantTools) {
		t.Fatalf("executor tools = %+v, want %+v", executor.Tools, wantTools)
	}
	wantDefaults := []string{AgentNamePlanner, AgentNameExecutor, AgentNameReviewer}
	for _, name := range wantDefaults {
		got := byName[name]
		if got.Name != name {
			t.Fatalf("default specs missing required role %q", name)
		}
	}
	reviewer, ok := byName[AgentNameReviewer]
	if !ok {
		t.Fatalf("reviewer default missing: %+v", specs)
	}
	wantReviewerTools := []string{AgentToolWorkspace, AgentToolShell, AgentToolMCP}
	if !equalStrings(reviewer.Tools, wantReviewerTools) {
		t.Fatalf("reviewer tools = %+v, want %+v", reviewer.Tools, wantReviewerTools)
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

func TestAgentSpecShellExecutionPolicy_DefaultRoles(t *testing.T) {
	t.Parallel()

	specs, err := NormalizeAgentSpecs(map[string]AgentSpec{})
	if err != nil {
		t.Fatalf("NormalizeAgentSpecs() error = %v", err)
	}
	byName := specsByName(specs)

	cases := []struct {
		name string
		spec AgentSpec
		want string
	}{
		{name: "planner", spec: byName[AgentNamePlanner], want: "workspace_write"},
		{name: "executor", spec: byName[AgentNameExecutor], want: "workspace_write"},
		{name: "reviewer", spec: byName[AgentNameReviewer], want: "workspace_write"},
		{name: "memory", spec: AgentSpec{Name: AgentNameMemory}, want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.spec.ShellExecutionPolicy(); got != tc.want {
				t.Fatalf("ShellExecutionPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentSpecShellExecutionPolicy_CustomByTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec AgentSpec
		want string
	}{
		{
			name: "workspace and shell",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace, AgentToolShell}},
			want: "workspace_write",
		},
		{
			name: "shell only",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolShell}},
			want: "read_only",
		},
		{
			name: "no shell",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace}},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.spec.ShellExecutionPolicy(); got != tc.want {
				t.Fatalf("ShellExecutionPolicy() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAgentSpecWorkspaceAccessPolicy_CustomByTools(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec AgentSpec
		want string
	}{
		{
			name: "workspace and shell",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace, AgentToolShell}},
			want: "read_write",
		},
		{
			name: "workspace only",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace}},
			want: "read_only",
		},
		{
			name: "no workspace",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolShell}},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.spec.WorkspaceAccessPolicy(); got != tc.want {
				t.Fatalf("WorkspaceAccessPolicy() = %q, want %q", got, tc.want)
			}
		})
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
