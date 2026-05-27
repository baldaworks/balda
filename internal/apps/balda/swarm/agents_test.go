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

func TestAgentSpecShellExecutionPolicy_DefaultRoles(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		spec AgentSpec
		want string
	}{
		{name: "planner", spec: AgentSpec{Name: AgentNamePlanner}, want: AgentShellPolicyNone},
		{name: "executor", spec: AgentSpec{Name: AgentNameExecutor}, want: AgentShellPolicyWorkspaceWrite},
		{name: "reviewer", spec: AgentSpec{Name: AgentNameReviewer}, want: AgentShellPolicyReadOnly},
		{name: "memory", spec: AgentSpec{Name: AgentNameMemory}, want: AgentShellPolicyNone},
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
			want: AgentShellPolicyWorkspaceWrite,
		},
		{
			name: "shell only",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolShell}},
			want: AgentShellPolicyReadOnly,
		},
		{
			name: "no shell",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace}},
			want: AgentShellPolicyNone,
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

func TestAllowedToolsForRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		role   string
		want   []string
		wantOK bool
	}{
		{name: "planner", role: AgentNamePlanner, want: []string{}, wantOK: true},
		{name: "executor", role: AgentNameExecutor, want: []string{AgentToolWorkspace, AgentToolShell, AgentToolMCP}, wantOK: true},
		{name: "reviewer alias", role: "validator", want: []string{AgentToolWorkspace, AgentToolShell}, wantOK: true},
		{name: "memory", role: AgentNameMemory, want: []string{AgentToolMemory}, wantOK: true},
		{name: "unknown", role: "custom", want: nil, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := AllowedToolsForRole(tt.role)
			if ok != tt.wantOK {
				t.Fatalf("AllowedToolsForRole(%q) ok = %t, want %t", tt.role, ok, tt.wantOK)
			}
			if !equalStrings(got, tt.want) {
				t.Fatalf("AllowedToolsForRole(%q) = %#v, want %#v", tt.role, got, tt.want)
			}
		})
	}
}

func TestWorkspaceAccessForRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		role   string
		want   string
		wantOK bool
	}{
		{name: "planner", role: AgentNamePlanner, want: AgentWorkspaceAccessNone, wantOK: true},
		{name: "executor alias", role: "worker", want: AgentWorkspaceAccessReadWrite, wantOK: true},
		{name: "reviewer", role: AgentNameReviewer, want: AgentWorkspaceAccessReadOnly, wantOK: true},
		{name: "memory", role: AgentNameMemory, want: AgentWorkspaceAccessNone, wantOK: true},
		{name: "unknown", role: "custom", want: "", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, ok := WorkspaceAccessForRole(tt.role)
			if ok != tt.wantOK {
				t.Fatalf("WorkspaceAccessForRole(%q) ok = %t, want %t", tt.role, ok, tt.wantOK)
			}
			if got != tt.want {
				t.Fatalf("WorkspaceAccessForRole(%q) = %q, want %q", tt.role, got, tt.want)
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
			want: AgentWorkspaceAccessReadWrite,
		},
		{
			name: "workspace only",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolWorkspace}},
			want: AgentWorkspaceAccessReadOnly,
		},
		{
			name: "no workspace",
			spec: AgentSpec{Name: "custom", Tools: []string{AgentToolShell}},
			want: AgentWorkspaceAccessNone,
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
