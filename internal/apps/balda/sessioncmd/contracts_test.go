package sessioncmd

import (
	"reflect"
	"testing"
)

func TestNormalizePublicRuntimeMetadata(t *testing.T) {
	got := NormalizePublicRuntimeMetadata(PublicRuntimeMetadata{
		ProviderID: " provider ", ProviderType: " codex ", ReasoningEffort: " high ",
		MCPServers: []string{" z ", "a", "z", ""},
	})
	want := PublicRuntimeMetadata{ProviderID: "provider", ProviderType: "codex", ReasoningEffort: "high", MCPServers: []string{"a", "z"}}
	if !reflect.DeepEqual(got, want) || got.Validate() != nil {
		t.Fatalf("normalized metadata = %#v, want %#v", got, want)
	}
}

func TestActivationValidateAndClone(t *testing.T) {
	const changed = "changed"
	a := Activation{
		Kind: ActivationCreated, SessionID: "session-1", Label: "balda",
		Runtime:       RuntimeSummary{PublicRuntimeMetadata: PublicRuntimeMetadata{ProviderID: "provider", MCPServers: []string{"mcp"}}, EffectiveModel: "model"},
		Configuration: ConfigurationSummary{ProviderID: "provider", ConfiguredModel: "configured", RequestedModel: ptr("requested"), MCPServers: []string{"mcp"}},
		Warnings:      []WarningCode{WarningWorkspaceSyncSkipped},
	}
	if err := a.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	clone := a.Clone()
	clone.Runtime.MCPServers[0] = changed
	clone.Configuration.MCPServers[0] = changed
	*clone.Configuration.RequestedModel = changed
	clone.Warnings[0] = changed
	if a.Runtime.MCPServers[0] != "mcp" || a.Configuration.MCPServers[0] != "mcp" || *a.Configuration.RequestedModel != "requested" || a.Warnings[0] != WarningWorkspaceSyncSkipped {
		t.Fatal("Clone aliases activation slices")
	}
}

func ptr(value string) *string { return &value }

func TestActivationValidateRejectsInvalidContracts(t *testing.T) {
	base := Activation{Kind: ActivationRestored, SessionID: "session-1"}
	for name, mutate := range map[string]func(*Activation){
		"kind":    func(a *Activation) { a.Kind = "unknown" },
		"session": func(a *Activation) { a.SessionID = "" },
		"warning": func(a *Activation) { a.Warnings = []WarningCode{"unknown"} },
		"duplicate": func(a *Activation) {
			a.Warnings = []WarningCode{WarningWorkspaceSyncSkipped, WarningWorkspaceSyncSkipped}
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := *base.Clone()
			mutate(&candidate)
			if candidate.Validate() == nil {
				t.Fatal("Validate() succeeded")
			}
		})
	}
}
