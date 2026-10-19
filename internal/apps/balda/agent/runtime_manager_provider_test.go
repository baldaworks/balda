package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
)

type providerStateFixture struct{ state providers.State }

func (s *providerStateFixture) Read(context.Context) (providers.State, error) { return s.state, nil }
func (s *providerStateFixture) Apply(context.Context, providers.Mutation) (providers.State, error) {
	return providers.State{}, errors.New("unexpected mutation")
}
func (s *providerStateFixture) Revisions(_ context.Context, refs []providers.RevisionRef) ([]providers.Revision, error) {
	result := make([]providers.Revision, 0, len(refs))
	for _, ref := range refs {
		found := false
		for _, revision := range s.state.Revisions {
			if revision.ProviderID == ref.ProviderID && revision.ID == ref.RevisionID {
				result = append(result, revision)
				found = true
			}
		}
		if !found {
			return nil, providers.ErrUnavailable
		}
	}
	return result, nil
}

type providerGraphCapture struct{ models []string }

type graphFactory struct {
	configs map[string]agentconfig.Config
	capture *providerGraphCapture
}

func (f *graphFactory) WithProviders(configs map[string]agentconfig.Config) ScopedRuntimeFactory {
	return &graphFactory{configs: maps.Clone(configs), capture: f.capture}
}

func (f *graphFactory) BuildScoped(_ context.Context, request agentfactory.BuildRequest, _ map[string][]string) (adkagent.Agent, error) {
	model := ""
	if cfg := f.configs[request.AgentID]; cfg.CodexACP != nil {
		model = cfg.CodexACP.Model
	}
	f.capture.models = append(f.capture.models, model)
	return adkagent.New(adkagent.Config{Name: request.Name, Description: request.Description})
}

func TestRuntimeManagerPinsAndRestoresProviderGraph(t *testing.T) {
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	revision := func(id, model string) providers.Revision {
		return providers.Revision{ID: id, ProviderID: "managed", DriverType: agentconfig.AgentTypeCodexACP,
			ContractVersion: 1, FormatVersion: providers.StorageFormatVersion, CreatedAt: now,
			Definition: providers.Definition{Config: agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: model}}, Reproducible: false}}
	}
	oldRevision, newRevision := revision("old", "old-model"), revision("new", "new-model")
	store := &providerStateFixture{state: providers.State{
		Records:   []providers.Record{{ID: "managed", CurrentRevisionID: oldRevision.ID, Enabled: true}},
		Revisions: []providers.Revision{oldRevision, newRevision},
	}}
	service := providers.New(nil, store, nil)
	capture := &providerGraphCapture{}
	scoped := &graphFactory{capture: capture}
	builder := &Builder{
		factory:       &recordingRuntimeFactory{},
		scopedFactory: scoped,
		normaCfg:      runtimeconfig.RuntimeConfig{},
	}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{SnapshotID: "snapshot", Skills: SkillMetadataProjection{Snapshot: "snapshot"}}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder: builder, BaldaProviderID: "managed", WorkingDir: t.TempDir(), StateDir: t.TempDir(),
		CapabilityBinder: binder, ProviderService: service, MCPRegistry: mcpregistry.New(nil), Logger: zerolog.Nop(),
	})

	fresh, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	var binding providers.Binding
	if fresh.ProviderBindingJSON == "" || json.Unmarshal([]byte(fresh.ProviderBindingJSON), &binding) != nil || binding.Entries[0].RevisionID != "old" {
		t.Fatalf("fresh binding = %q", fresh.ProviderBindingJSON)
	}

	store.state.Records[0].CurrentRevisionID = newRevision.ID
	current, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = current.Close() }()
	store.state.Records[0].Enabled = false
	restored, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{ProviderBindingJSON: fresh.ProviderBindingJSON})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if _, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{}); !errors.Is(err, providers.ErrUnavailable) {
		t.Fatalf("disabled current graph error = %v", err)
	}
	if got, want := capture.models, []string{"old-model", "new-model", "old-model"}; !slicesEqualStrings(got, want) {
		t.Fatalf("built models = %v, want %v", got, want)
	}
}

func slicesEqualStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
