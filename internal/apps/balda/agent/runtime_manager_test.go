package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
)

func TestPinnedRuntimeMCPServerIDsPreservesHostConfiguration(t *testing.T) {
	t.Parallel()
	got := pinnedRuntimeMCPServerIDs(
		[]string{" host.one ", "shared", "host.one"},
		[]string{"snapshot.old", "shared"},
	)
	want := []string{"host.one", "shared", "snapshot.old"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pinnedRuntimeMCPServerIDs() = %#v, want %#v", got, want)
	}
}

func TestRuntimeManagerAllowsPreflightWithoutCapabilityBinder(t *testing.T) {
	t.Parallel()
	field, ok := reflect.TypeOf(RuntimeManagerParams{}).FieldByName("CapabilityBinder")
	if !ok {
		t.Fatal("RuntimeManagerParams.CapabilityBinder field is missing")
	}
	if got := field.Tag.Get("optional"); got != "true" {
		t.Fatalf("CapabilityBinder optional tag = %q, want true", got)
	}
}

type recordingCapabilityBinder struct {
	binding  SessionCapabilityBinding
	err      error
	requests []SessionRuntimeRequest
}

type recordingRuntimeFactory struct {
	requests []agentfactory.BuildRequest
}

func (f *recordingRuntimeFactory) Build(_ context.Context, request agentfactory.BuildRequest) (adkagent.Agent, error) {
	f.requests = append(f.requests, request)
	return adkagent.New(adkagent.Config{Name: request.Name, Description: request.Description})
}

func TestRuntimeForSessionProjectsSkillMetadataAndExtensionInstructionsToProviderRequest(t *testing.T) {
	factory := &recordingRuntimeFactory{}
	var contributorInput SessionInstructionContext
	builder := &Builder{
		factory: factory,
		instructionContributors: []SessionInstructionContributor{
			&testInstructionContributor{id: "example.extension", content: "Extension guidance.", input: &contributorInput},
		},
	}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-prism",
		Skills: SkillMetadataProjection{
			Snapshot: "snapshot-prism",
			Skills: []SkillPromptMetadata{{
				Source:      runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"},
				Name:        "story",
				Description: "Run a Prism story.",
				Revision:    "revision-prism",
			}},
		},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		CapabilityBinder: binder,
		MCPRegistry:      mcpregistry.New(nil),
		Logger:           zerolog.Nop(),
	})

	runtime, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{
		Locator: deliverycmd.Locator{SessionID: "session-1", ChannelType: "telegram"},
	})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	t.Cleanup(func() { _ = runtime.Close() })
	if len(factory.requests) != 1 {
		t.Fatalf("provider build requests = %d, want 1", len(factory.requests))
	}
	instruction := factory.requests[0].Instruction
	for _, want := range []string{
		`source_kind="plugin" source_name="prism" skill_name="story" revision="revision-prism"`,
		"Run a Prism story.",
		"Extension instruction [example.extension]:\nExtension guidance.",
	} {
		if !strings.Contains(instruction, want) {
			t.Fatalf("provider instruction missing %q:\n%s", want, instruction)
		}
	}
	if contributorInput.SnapshotID != "snapshot-prism" || contributorInput.SessionID != "session-1" || contributorInput.ChannelType != "telegram" {
		t.Fatalf("contributor input = %+v, want pinned session context", contributorInput)
	}
}

func (b *recordingCapabilityBinder) BindSessionCapabilities(_ context.Context, request SessionRuntimeRequest) (SessionCapabilityBinding, error) {
	b.requests = append(b.requests, request)
	return b.binding, b.err
}

func TestRuntimeForSessionOwnsCapabilityBinding(t *testing.T) {
	providers := map[string]agentconfig.Config{"alpha": {
		Type:   agentconfig.AgentTypeOpenAI,
		OpenAI: &agentconfig.LocalAPIConfig{APIKey: "test-key", Model: "gpt-4o-mini"},
	}}
	registry := mcpregistry.New(map[string]agentconfig.MCPServerConfig{
		"balda": {Type: agentconfig.MCPServerTypeHTTP, URL: "http://127.0.0.1:1/mcp"},
	})
	builder := &Builder{
		factory:  agentfactory.New(providers, registry),
		normaCfg: runtimeconfig.RuntimeConfig{Providers: providers},
	}
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-1"},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:          builder,
		BaldaProviderID:  "alpha",
		WorkingDir:       t.TempDir(),
		StateDir:         t.TempDir(),
		CapabilityBinder: binder,
		MCPRegistry:      registry,
		Logger:           zerolog.Nop(),
	})

	runtime, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{RuntimeSnapshotID: "snapshot-1"})
	if err != nil {
		t.Fatalf("RuntimeForSession() error = %v", err)
	}
	if runtime.RuntimeSnapshotID != "snapshot-1" {
		t.Fatalf("runtime snapshot = %q, want snapshot-1", runtime.RuntimeSnapshotID)
	}
	if len(binder.requests) != 1 || binder.requests[0].RuntimeSnapshotID != "snapshot-1" {
		t.Fatalf("capability requests = %+v, want persisted snapshot", binder.requests)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := runtime.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

func TestRuntimeForSessionRejectsIncompleteCapabilityBinding(t *testing.T) {
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: runtimecatalogcmd.SnapshotID("snapshot-other")},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := &RuntimeManager{capabilityBinder: binder}

	_, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{})
	if err == nil || !strings.Contains(err.Error(), "binding is incomplete") {
		t.Fatalf("RuntimeForSession() error = %v, want incomplete binding", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

func TestRuntimeForSessionReleasesCapabilitiesOnConstructionFailure(t *testing.T) {
	closeCalls := 0
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{
		SnapshotID: "snapshot-1",
		Skills:     SkillMetadataProjection{Snapshot: "snapshot-1"},
		Close: func() error {
			closeCalls++
			return nil
		},
	}}
	manager := &RuntimeManager{capabilityBinder: binder}

	_, err := manager.RuntimeForSession(context.Background(), SessionRuntimeRequest{})
	if err == nil || !strings.Contains(err.Error(), "agent builder is required") {
		t.Fatalf("RuntimeForSession() error = %v, want missing builder", err)
	}
	if closeCalls != 1 {
		t.Fatalf("capability close calls = %d, want 1", closeCalls)
	}
}

type closeableRuntimeAgent struct {
	closeErr error
}

func (a *closeableRuntimeAgent) Close() error {
	return a.closeErr
}

func TestCloseRuntimeAgent_IgnoresExpectedShutdownError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close acp client: acp client close: context canceled")}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}

func TestCloseRuntimeAgent_ReturnsUnexpectedCloseError(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed")}

	err := closeRuntimeAgent(agent)
	if err == nil {
		t.Fatal("closeRuntimeAgent() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "close balda runtime agent: close failed") {
		t.Fatalf("closeRuntimeAgent() error = %v, want wrapped close failure", err)
	}
}

func TestCloseRuntimeAgent_IgnoresWrappedContextCanceled(t *testing.T) {
	agent := &closeableRuntimeAgent{closeErr: fmt.Errorf("close failed: %w", context.Canceled)}

	if err := closeRuntimeAgent(agent); err != nil {
		t.Fatalf("closeRuntimeAgent() error = %v, want nil", err)
	}
}
