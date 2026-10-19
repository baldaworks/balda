package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/secrets"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

type memoryStore struct{ state State }

func (s *memoryStore) Read(context.Context) (State, error) { return cloneState(s.state), nil }

func (s *memoryStore) Apply(_ context.Context, mutation Mutation) (State, error) {
	if s.state.Version != mutation.ExpectedVersion {
		return State{}, ErrConflict
	}
	replaced := false
	for i := range s.state.Records {
		if s.state.Records[i].ID == mutation.Record.ID {
			s.state.Records[i] = mutation.Record
			replaced = true
		}
	}
	if !replaced {
		s.state.Records = append(s.state.Records, mutation.Record)
	}
	if mutation.Revision != nil {
		s.state.Revisions = append(s.state.Revisions, *mutation.Revision)
	}
	s.state.Version++
	return cloneState(s.state), nil
}

func (s *memoryStore) Revisions(_ context.Context, refs []RevisionRef) ([]Revision, error) {
	result := make([]Revision, 0, len(refs))
	for _, ref := range refs {
		revision := findRevision(s.state.Revisions, ref.ProviderID, ref.RevisionID)
		if revision == nil {
			return nil, ErrUnavailable
		}
		result = append(result, *revision)
	}
	return result, nil
}

func cloneState(state State) State {
	state.Records = append([]Record(nil), state.Records...)
	state.Revisions = append([]Revision(nil), state.Revisions...)
	return state
}

func newTestService(t *testing.T, configured map[string]agentconfig.Config, store *memoryStore) *Service {
	t.Helper()
	protector, err := secrets.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	service := New(configured, store, protector)
	nextID := 0
	service.newID = func() string {
		nextID++
		return "revision-" + string(rune('0'+nextID))
	}
	return service
}

func codexConfig(model string) agentconfig.Config {
	return agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: model}}
}

func TestServiceCompilesOrderedMixedGraphWithoutExposingSecrets(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	configured := map[string]agentconfig.Config{
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "configured-secret", Model: "gpt"}},
	}
	service := newTestService(t, configured, store)
	secret := "managed-secret"
	state, err := service.Create(ctx, Change{ID: "worker", Config: codexConfig("codex"), Protected: ProtectedEdit{Environment: map[string]*string{"CODEX_HOME": &secret}}})
	if err != nil {
		t.Fatal(err)
	}
	pool := agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"worker", "hosted"}}}
	if _, err := service.Create(ctx, Change{ExpectedVersion: state.Version, ID: "root", Config: pool}); err != nil {
		t.Fatal(err)
	}
	compiled, err := service.CompileCurrent(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	wantOrder := []string{"root", "worker", "hosted"}
	gotOrder := make([]string, 0, len(compiled.Binding.Entries))
	for _, entry := range compiled.Binding.Entries {
		gotOrder = append(gotOrder, entry.ProviderID)
	}
	if !slices.Equal(gotOrder, wantOrder) {
		t.Fatalf("binding order = %v, want %v", gotOrder, wantOrder)
	}
	if compiled.Binding.Reproducible {
		t.Fatal("mixed configured graph reported reproducible")
	}
	if got := compiled.Configs["worker"].CodexACP.Env["CODEX_HOME"]; got != secret {
		t.Fatalf("restored environment = %q", got)
	}
	public, _ := json.Marshal(store.state)
	if bytes.Contains(public, []byte(secret)) || bytes.Contains(public, []byte("configured-secret")) {
		t.Fatalf("public state leaked a secret: %s", public)
	}
}

func TestInventoryProjectsConfiguredAndManagedWithoutProtectedPayloads(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(t, map[string]agentconfig.Config{
		"configured": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "configured-secret", Model: "gpt"}},
	}, store)
	secret := "managed-secret"
	if _, err := service.Create(t.Context(), Change{ID: "managed", Config: codexConfig("codex"), Protected: ProtectedEdit{Environment: map[string]*string{"TOKEN": &secret}}}); err != nil {
		t.Fatal(err)
	}
	inventory, err := service.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if inventory.Version != 1 || len(inventory.Items) != 2 {
		t.Fatalf("inventory = %+v", inventory)
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("configured-secret")) || bytes.Contains(encoded, []byte("managed-secret")) || bytes.Contains(encoded, store.state.Revisions[0].ProtectedValues) {
		t.Fatalf("inventory leaked protected material: %s", encoded)
	}
	if !inventory.Items[0].Revision.Definition.HasAPIKey || !slices.Equal(inventory.Items[1].Revision.Definition.EnvironmentNames, []string{"TOKEN"}) {
		t.Fatalf("inventory lost protected presence: %+v", inventory.Items)
	}
}

func TestInventoryRejectsSourceCollision(t *testing.T) {
	store := &memoryStore{state: State{
		Version: 1,
		Records: []Record{{ID: "duplicate", CurrentRevisionID: "revision", Enabled: true}},
		Revisions: []Revision{{ID: "revision", ProviderID: "duplicate", DriverType: agentconfig.AgentTypeCodexACP,
			Definition: Definition{Config: codexConfig("managed")}}},
	}}
	service := newTestService(t, map[string]agentconfig.Config{"duplicate": codexConfig("configured")}, store)
	if _, err := service.Inventory(t.Context()); !errors.Is(err, ErrInvalid) {
		t.Fatalf("inventory collision error = %v, want ErrInvalid", err)
	}
}

func TestServiceRestoresExactManagedRevisionAndRejectsChangedConfiguredDefinition(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	configured := map[string]agentconfig.Config{"yaml": codexConfig("first")}
	service := newTestService(t, configured, store)
	state, err := service.Create(ctx, Change{ID: "managed", Config: codexConfig("old")})
	if err != nil {
		t.Fatal(err)
	}
	pool := agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"managed", "yaml"}}}
	if _, err := service.Create(ctx, Change{ExpectedVersion: state.Version, ID: "root", Config: pool}); err != nil {
		t.Fatal(err)
	}
	before, err := service.CompileCurrent(ctx, "root")
	if err != nil {
		t.Fatal(err)
	}
	current := store.state.Version
	if _, err := service.Update(ctx, Change{ExpectedVersion: current, ID: "managed", Config: codexConfig("new")}); err != nil {
		t.Fatal(err)
	}
	restored, err := service.Restore(ctx, before.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Configs["managed"].CodexACP.Model; got != "old" {
		t.Fatalf("restored model = %q, want old", got)
	}
	changed := newTestService(t, map[string]agentconfig.Config{"yaml": codexConfig("second")}, store)
	if _, err := changed.Restore(ctx, before.Binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("changed configured restore error = %v", err)
	}
}

func TestServiceRejectsUnsafeManagedDefinitionsAndBrokenGraphs(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	service := newTestService(t, nil, store)

	tests := []struct {
		name string
		id   string
		cfg  agentconfig.Config
	}{
		{name: "generic", id: "generic", cfg: agentconfig.Config{Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"agent"}}}},
		{name: "command override", id: "command", cfg: agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Cmd: []string{"agent"}}}},
		{name: "plaintext environment", id: "plaintext", cfg: agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Env: map[string]string{"TOKEN": "leak"}}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Create(ctx, Change{ID: test.id, Config: test.cfg}); !errors.Is(err, ErrInvalid) {
				t.Fatalf("Create() error = %v", err)
			}
		})
	}

	state, err := service.Create(ctx, Change{ID: "leaf", Config: codexConfig("model")})
	if err != nil {
		t.Fatal(err)
	}
	pool := agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"leaf", "leaf"}}}
	if _, err := service.Create(ctx, Change{ExpectedVersion: state.Version, ID: "duplicates", Config: pool}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("duplicate members error = %v", err)
	}
	validPool := agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"leaf"}}}
	state, err = service.Create(ctx, Change{ExpectedVersion: state.Version, ID: "root", Config: validPool})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.SetEnabled(ctx, state.Version, "leaf", false, usercmd.AuditEvent{}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("disable referenced member error = %v", err)
	}
}

func TestCompileRejectsSourceCollisionAndCycle(t *testing.T) {
	store := &memoryStore{state: State{
		Records: []Record{{ID: "loop", CurrentRevisionID: "revision", Enabled: true}},
		Revisions: []Revision{{
			ID: "revision", ProviderID: "loop", DriverType: agentconfig.AgentTypePool,
			ContractVersion: 1, FormatVersion: StorageFormatVersion,
			Definition: Definition{Config: agentconfig.Config{Type: agentconfig.AgentTypePool, PoolConfig: &agentconfig.PoolConfig{Members: []string{"loop"}}}, Reproducible: true},
		}},
	}}
	service := newTestService(t, nil, store)
	if _, err := service.CompileCurrent(t.Context(), "loop"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("cycle error = %v", err)
	}
	service = newTestService(t, map[string]agentconfig.Config{"loop": codexConfig("configured")}, store)
	if _, err := service.CompileCurrent(t.Context(), "loop"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("source collision error = %v", err)
	}
}

func TestRestoreRejectsUnavailableContractAndTampering(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	service := newTestService(t, nil, store)
	secret := "value"
	if _, err := service.Create(ctx, Change{ID: "managed", Config: codexConfig("model"), Protected: ProtectedEdit{Environment: map[string]*string{"TOKEN": &secret}}}); err != nil {
		t.Fatal(err)
	}
	compiled, err := service.CompileCurrent(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	store.state.Revisions[0].ContractVersion++
	if _, err := service.Restore(ctx, compiled.Binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("contract mismatch error = %v", err)
	}
	store.state.Revisions[0].ContractVersion--
	store.state.Revisions[0].ProtectedValues[len(store.state.Revisions[0].ProtectedValues)-1] ^= 1
	if _, err := service.Restore(ctx, compiled.Binding); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("tampered protected values error = %v", err)
	}
}

func TestManagedReproducibilityTracksInheritedEnvironment(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	service := newTestService(t, nil, store)
	inherit := false
	cfg := codexConfig("model")
	cfg.CodexACP.InheritEnv = &inherit
	if _, err := service.Create(ctx, Change{ID: "isolated", Config: cfg}); err != nil {
		t.Fatal(err)
	}
	compiled, err := service.CompileCurrent(ctx, "isolated")
	if err != nil {
		t.Fatal(err)
	}
	if !compiled.Binding.Reproducible || len(compiled.Binding.Warnings) != 0 {
		t.Fatalf("isolated binding = %+v", compiled.Binding)
	}
}
