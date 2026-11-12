package sessionfmt

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/sessioncmd"
)

func testActivation(kind sessioncmd.ActivationKind) sessioncmd.Activation {
	return sessioncmd.Activation{
		Kind: kind, SessionID: "session-1", Label: "balda",
		Runtime: sessioncmd.RuntimeSummary{PublicRuntimeMetadata: sessioncmd.PublicRuntimeMetadata{ProviderID: "provider", MCPServers: []string{"mcp"}}, EffectiveModel: "model"},
	}
}

func TestNewNotice(t *testing.T) {
	for _, kind := range []sessioncmd.ActivationKind{sessioncmd.ActivationCreated, sessioncmd.ActivationReplaced} {
		notice, err := NewNotice(testActivation(kind))
		if err != nil || notice.Kind != kind {
			t.Fatalf("NewNotice(%q) = %#v, %v", kind, notice, err)
		}
		envelope, err := notice.Envelope()
		if err != nil || envelope.Descriptor.Type != "balda.session.activation.v1" {
			t.Fatalf("Envelope() = %#v, %v", envelope, err)
		}
	}
	if _, err := NewNotice(testActivation(sessioncmd.ActivationRestored)); err == nil {
		t.Fatal("restored activation rendered as notice")
	}
}

func TestNewNoticeClassifiesColdConfiguration(t *testing.T) {
	requested := "model-requested"
	activation := sessioncmd.Activation{
		Kind: sessioncmd.ActivationCreated, SessionID: "session-1",
		Configuration: sessioncmd.ConfigurationSummary{
			ProviderID: "provider", ConfiguredModel: "model-default", RequestedModel: &requested,
			ReasoningEffort: "high", MCPServers: []string{"grafana"},
		},
	}
	notice, err := NewNotice(activation)
	if err != nil || notice.ModelState != ModelStateRequestedPending || notice.ProviderID() != "provider" || notice.ReasoningEffort() != "high" {
		t.Fatalf("NewNotice() = %#v, %v", notice, err)
	}
	label, model := notice.ModelDisplay()
	if label != "Model (requested)" || model != requested || len(notice.MCPServers()) != 1 || notice.MCPServers()[0] != "grafana" {
		t.Fatalf("cold display = %q/%q/%v", label, model, notice.MCPServers())
	}
}

func TestWarning(t *testing.T) {
	warning, err := NewWarning("session-1", sessioncmd.WarningWorkspaceSyncSkipped)
	if err != nil {
		t.Fatalf("NewWarning() error = %v", err)
	}
	envelope, err := warning.Envelope()
	if err != nil || envelope.Descriptor.Type != "balda.session.activation-warning.v1" {
		t.Fatalf("Envelope() = %#v, %v", envelope, err)
	}
	if _, err := NewWarning("session-1", "unknown"); err == nil {
		t.Fatal("unknown warning accepted")
	}
}
