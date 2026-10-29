package agent

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/agentfactory"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
)

type providerStateFixture struct {
	state     providers.State
	readCalls int
}

func (s *providerStateFixture) Read(context.Context) (providers.State, error) {
	s.readCalls++
	return s.state, nil
}
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

type sessionFailingGraphFactory struct {
	err error
}

func (f *sessionFailingGraphFactory) WithProviders(map[string]agentconfig.Config) ScopedRuntimeFactory {
	return f
}

func (f *sessionFailingGraphFactory) BuildScoped(_ context.Context, request agentfactory.BuildRequest, _ map[string][]string) (adkagent.Agent, error) {
	return adkagent.New(adkagent.Config{Name: request.Name, Description: request.Description})
}

func (f *sessionFailingGraphFactory) BuildSession(context.Context, agentfactory.BuildRequest, map[string][]string, string) (adkagent.Agent, error) {
	return nil, f.err
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
		normaCfg: runtimeconfig.RuntimeConfig{Providers: map[string]agentconfig.Config{
			"managed": {Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: "root-display-model"}},
		}},
	}
	binder := &recordingCapabilityBinder{binding: SessionCapabilityBinding{SnapshotID: "snapshot", Skills: SkillMetadataProjection{Snapshot: "snapshot"}}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder: builder, BaldaProviderID: "managed", WorkingDir: t.TempDir(), StateDir: t.TempDir(),
		CapabilityBinder: binder, ProviderService: service, MCPRegistry: mcpregistry.New(nil), Logger: zerolog.Nop(),
	})
	if store.readCalls != 0 {
		t.Fatalf("provider reads during construction = %d, want 0", store.readCalls)
	}

	fresh, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = fresh.Close() }()
	var binding providers.Binding
	if fresh.ProviderBindingJSON == "" || json.Unmarshal([]byte(fresh.ProviderBindingJSON), &binding) != nil || binding.Entries[0].RevisionID != "old" {
		t.Fatalf("fresh binding = %q", fresh.ProviderBindingJSON)
	}
	if fresh.RecipeModel != "old-model" {
		t.Fatalf("fresh recipe receipt = %q, want managed old-model", fresh.RecipeModel)
	}

	store.state.Records[0].CurrentRevisionID = newRevision.ID
	current, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = current.Close() }()
	if current.RecipeModel != "new-model" {
		t.Fatalf("current recipe receipt = %q, want managed new-model", current.RecipeModel)
	}
	store.state.Records[0].Enabled = false
	restored, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{ProviderBindingJSON: fresh.ProviderBindingJSON})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = restored.Close() }()
	if restored.RecipeModel != "old-model" {
		t.Fatalf("restored recipe receipt = %q, want pinned old-model", restored.RecipeModel)
	}
	if _, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{}); !errors.Is(err, providers.ErrUnavailable) {
		t.Fatalf("disabled current graph error = %v", err)
	}
	if got, want := capture.models, []string{"old-model", "new-model", "old-model"}; !slicesEqualStrings(got, want) {
		t.Fatalf("built models = %v, want %v", got, want)
	}
}

func TestRuntimeManagerReportsEmptyProviderAsUnavailable(t *testing.T) {
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:     &Builder{},
		WorkingDir:  t.TempDir(),
		StateDir:    t.TempDir(),
		MCPRegistry: mcpregistry.New(nil),
		Logger:      zerolog.Nop(),
	})

	if _, err := manager.Runtime(t.Context()); !errors.Is(err, ErrProviderUnavailable) {
		t.Fatalf("Runtime() error = %v, want ErrProviderUnavailable", err)
	}
}

func TestRuntimeManagerDefersUnavailableManagedProviderUntilFirstUse(t *testing.T) {
	store := &providerStateFixture{state: providers.State{
		Records: []providers.Record{{ID: "managed", CurrentRevisionID: "deleted", Deleted: true}},
	}}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder:         &Builder{scopedFactory: &graphFactory{capture: &providerGraphCapture{}}},
		BaldaProviderID: "managed",
		WorkingDir:      t.TempDir(),
		StateDir:        t.TempDir(),
		ProviderService: providers.New(nil, store, nil),
		MCPRegistry:     mcpregistry.New(nil),
		Logger:          zerolog.Nop(),
	})
	if store.readCalls != 0 {
		t.Fatalf("provider reads during construction = %d, want 0", store.readCalls)
	}

	_, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{})
	if !errors.Is(err, ErrProviderUnavailable) || !errors.Is(err, providers.ErrUnavailable) {
		t.Fatalf("RuntimeForSession() error = %v, want typed unavailable provider error", err)
	}
	if store.readCalls != 1 {
		t.Fatalf("provider reads after first use = %d, want 1", store.readCalls)
	}
}

func TestRuntimeManagerReportsSessionProviderLaunchFailureAsUnavailable(t *testing.T) {
	launchErr := errors.New("launch provider session")
	factory := &sessionFailingGraphFactory{err: launchErr}
	manager := NewRuntimeManager(RuntimeManagerParams{
		Builder: &Builder{
			factory:       &recordingRuntimeFactory{},
			scopedFactory: factory,
		},
		BaldaProviderID: "configured",
		WorkingDir:      t.TempDir(),
		StateDir:        t.TempDir(),
		CapabilityBinder: &recordingCapabilityBinder{binding: SessionCapabilityBinding{
			SnapshotID: "snapshot",
			Skills:     SkillMetadataProjection{Snapshot: "snapshot"},
		}},
		MCPRegistry: mcpregistry.New(nil),
		Logger:      zerolog.Nop(),
	})

	if err := manager.EnsureProviderAvailable(t.Context()); err != nil {
		t.Fatalf("EnsureProviderAvailable() error = %v, want app runtime available", err)
	}
	_, err := manager.RuntimeForSession(t.Context(), SessionRuntimeRequest{
		Locator:        deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", SessionID: "tg-1-0"},
		UserID:         "telegram:1",
		AgentSessionID: "tg-1-0",
	})
	if !errors.Is(err, ErrProviderUnavailable) || !errors.Is(err, launchErr) {
		t.Fatalf("RuntimeForSession() error = %v, want typed provider launch failure", err)
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
