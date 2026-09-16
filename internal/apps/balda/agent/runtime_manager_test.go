package agent

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	runtimeconfig "github.com/normahq/runtime/v2/appconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
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
