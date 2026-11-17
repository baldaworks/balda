package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
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

func (s *memoryStore) ListRevisions(context.Context) ([]Revision, error) {
	return cloneState(s.state).Revisions, nil
}

func cloneState(state State) State {
	state.Records = append([]Record(nil), state.Records...)
	state.Revisions = append([]Revision(nil), state.Revisions...)
	for i := range state.Revisions {
		state.Revisions[i].ProtectedValues = bytes.Clone(state.Revisions[i].ProtectedValues)
	}
	return state
}

func newTestService(t *testing.T, configured map[string]agentconfig.Config, store *memoryStore) *Service {
	t.Helper()
	protector, err := newProviderProtector(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32)))
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

func TestProviderCredentialsReadV1WriteV2AndRejectMCPScope(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{6}, 32))
	// Deprecated root-direct encryption is required only to build a retained v1 fixture.
	//nolint:staticcheck
	legacyProtector, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{}
	legacy := New(nil, store, legacyProtector)
	legacy.newID = func() string { return "legacy-revision" }
	apiKey := "legacy-provider-secret"
	if _, err := legacy.Create(t.Context(), Change{ID: "legacy", Config: openAIConfig("legacy"), Protected: ProtectedEdit{APIKey: &apiKey}}); err != nil {
		t.Fatal(err)
	}
	legacyRevision := store.state.Revisions[0]
	if legacyRevision.ProtectedValues[0] != 1 {
		t.Fatalf("legacy provider frame = %d, want v1", legacyRevision.ProtectedValues[0])
	}
	root, err := secrets.NewRoot(key)
	if err != nil {
		t.Fatal(err)
	}
	providerProtector, err := root.Protector(secrets.ScopeProviderRevision)
	if err != nil {
		t.Fatal(err)
	}
	scoped := New(nil, store, providerProtector)
	values, err := scoped.open(legacyRevision)
	if err != nil || values.APIKey != apiKey {
		t.Fatalf("v1 provider revision unavailable: %#v, %v", values, err)
	}
	scoped.newID = func() string { return "scoped-revision" }
	if _, err := scoped.Create(t.Context(), Change{ExpectedVersion: store.state.Version, ID: "scoped", Config: openAIConfig("scoped"), Protected: ProtectedEdit{APIKey: &apiKey}}); err != nil {
		t.Fatal(err)
	}
	scopedRevision := store.state.Revisions[1]
	if scopedRevision.ProtectedValues[0] != 2 {
		t.Fatalf("scoped provider frame = %d, want v2", scopedRevision.ProtectedValues[0])
	}
	plain, err := providerProtector.Open(scopedRevision.ProtectedValues, revisionAAD(scopedRevision.ProviderID, scopedRevision.ID))
	if err != nil {
		t.Fatal(err)
	}
	mcpProtector, err := root.Protector(secrets.ScopeMCPRevision)
	if err != nil {
		t.Fatal(err)
	}
	crossScope, err := mcpProtector.Seal(plain, revisionAAD(scopedRevision.ProviderID, scopedRevision.ID))
	clear(plain)
	if err != nil {
		t.Fatal(err)
	}
	scopedRevision.ProtectedValues = crossScope
	if _, err := scoped.open(scopedRevision); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("MCP-scoped payload accepted as provider credentials: %v", err)
	}
}

func TestValidateCredentialsChecksEveryRetainedRevisionWithoutMutation(t *testing.T) {
	key := base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{8}, 32))
	// Deprecated root-direct encryption is required only to build a retained v1 fixture.
	//nolint:staticcheck
	legacy, err := secrets.New(key)
	if err != nil {
		t.Fatal(err)
	}
	root, err := secrets.NewRoot(key)
	if err != nil {
		t.Fatal(err)
	}
	scoped, err := root.Protector(secrets.ScopeProviderRevision)
	if err != nil {
		t.Fatal(err)
	}
	plain := []byte(`{"api_key":"retained-secret"}`)
	v1, err := legacy.Seal(plain, revisionAAD("retained", "revision-v1"))
	if err != nil {
		t.Fatal(err)
	}
	v2, err := scoped.Seal(plain, revisionAAD("retained", "revision-v2"))
	clear(plain)
	if err != nil {
		t.Fatal(err)
	}
	base := State{Version: 7,
		Records: []Record{{ID: "retained", CurrentRevisionID: "revision-v2", Enabled: true}},
		Revisions: []Revision{
			{ProviderID: "retained", ID: "revision-v1", Definition: Definition{HasAPIKey: true}, ProtectedValues: v1},
			{ProviderID: "retained", ID: "revision-v2", Definition: Definition{HasAPIKey: true}, ProtectedValues: v2},
		},
	}
	store := &memoryStore{state: cloneState(base)}
	service := New(nil, store, scoped)
	if err := service.ValidateCredentials(t.Context()); err != nil {
		t.Fatalf("mixed retained credentials rejected: %v", err)
	}
	if !reflect.DeepEqual(store.state, base) {
		t.Fatal("passive retained-state validation mutated provider state")
	}

	for _, tc := range []struct {
		name      string
		protector *secrets.Protector
		mutate    func(*State)
	}{
		{name: "missing root", protector: newProviderProtectorForTest(t, "")},
		{name: "wrong root", protector: newProviderProtectorForTest(t, base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))},
		{name: "corrupt frame", protector: scoped, mutate: func(state *State) { state.Revisions[0].ProtectedValues[len(v1)-1] ^= 1 }},
		{name: "unknown version", protector: scoped, mutate: func(state *State) { state.Revisions[1].ProtectedValues[0] = 99 }},
		{name: "truncated frame", protector: scoped, mutate: func(state *State) { state.Revisions[1].ProtectedValues = state.Revisions[1].ProtectedValues[:2] }},
		{name: "missing ciphertext", protector: scoped, mutate: func(state *State) { state.Revisions[1].ProtectedValues = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			candidate := cloneState(base)
			if tc.mutate != nil {
				tc.mutate(&candidate)
			}
			if err := New(nil, &memoryStore{state: candidate}, tc.protector).ValidateCredentials(t.Context()); !errors.Is(err, ErrUnavailable) {
				t.Fatalf("retained credential failure = %v, want unavailable", err)
			}
		})
	}
}

func newProviderProtectorForTest(t *testing.T, key string) *secrets.Protector {
	t.Helper()
	root, err := secrets.NewRoot(key)
	if err != nil {
		t.Fatal(err)
	}
	protector, err := root.Protector(secrets.ScopeProviderRevision)
	if err != nil {
		t.Fatal(err)
	}
	return protector
}

func codexConfig(model string) agentconfig.Config {
	return agentconfig.Config{Type: agentconfig.AgentTypeCodexACP, CodexACP: &agentconfig.ACPConfig{Model: model}}
}

func openAIConfig(model string) agentconfig.Config {
	return agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: model}}
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

func TestProviderLifecycleFiltersPassiveDetailAndRecreate(t *testing.T) {
	ctx := t.Context()
	store := &memoryStore{}
	service := newTestService(t, map[string]agentconfig.Config{
		"configured": codexConfig("configured-model"),
	}, store)
	oldSecret := "old-secret"
	created, err := service.Create(ctx, Change{ID: "managed", Config: codexConfig("old-model"), Protected: ProtectedEdit{
		Environment: map[string]*string{"TOKEN": &oldSecret},
	}})
	if err != nil {
		t.Fatal(err)
	}
	oldRevisionID := created.Records[0].CurrentRevisionID
	pinned, err := service.CompileCurrent(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.Delete(ctx, created.Version, "managed", usercmd.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}

	active, err := service.Inventory(ctx)
	if err != nil || len(active.Items) != 1 || active.Items[0].Source != SourceConfigured {
		t.Fatalf("active inventory = %+v, %v", active, err)
	}
	deletedInventory, err := service.InventoryByFilter(ctx, InventoryDeleted)
	if err != nil || len(deletedInventory.Items) != 1 || !deletedInventory.Items[0].Record.Deleted {
		t.Fatalf("deleted inventory = %+v, %v", deletedInventory, err)
	}
	all, err := service.InventoryByFilter(ctx, InventoryAll)
	if err != nil || len(all.Items) != 2 {
		t.Fatalf("all inventory = %+v, %v", all, err)
	}
	beforeDetail := cloneState(store.state)
	detail, version, err := service.Get(ctx, "managed")
	if err != nil || version != deleted.Version || !detail.Record.Deleted || len(detail.Revision.ProtectedValues) != 0 {
		t.Fatalf("deleted detail = %+v, version %d, %v", detail, version, err)
	}
	if !slices.Equal(beforeDetail.Records, store.state.Records) || len(beforeDetail.Revisions) != len(store.state.Revisions) || beforeDetail.Version != store.state.Version {
		t.Fatal("passive deleted detail changed provider state")
	}
	if _, _, err := service.Get(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing detail error = %v, want ErrNotFound", err)
	}
	if _, err := service.InventoryByFilter(ctx, InventoryFilter("unknown")); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown inventory filter error = %v, want ErrInvalid", err)
	}

	recreated, err := service.Create(ctx, Change{ExpectedVersion: deleted.Version, ID: "managed", Config: codexConfig("new-model")})
	if err != nil {
		t.Fatal(err)
	}
	current := findRecord(recreated.Records, "managed")
	if current == nil || current.Deleted || !current.Enabled || current.CurrentRevisionID == oldRevisionID {
		t.Fatalf("recreated record = %+v", current)
	}
	currentRevision := findRevision(recreated.Revisions, "managed", current.CurrentRevisionID)
	if currentRevision == nil || currentRevision.Definition.HasAPIKey || len(currentRevision.Definition.EnvironmentNames) != 0 || len(currentRevision.ProtectedValues) != 0 {
		t.Fatalf("recreated revision inherited protected values: %+v", currentRevision)
	}
	compiled, err := service.CompileCurrent(ctx, "managed")
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Configs["managed"].CodexACP.Model; got != "new-model" {
		t.Fatalf("current model = %q, want new-model", got)
	}
	if got := compiled.Configs["managed"].CodexACP.Env["TOKEN"]; got != "" {
		t.Fatalf("recreated environment inherited TOKEN = %q", got)
	}
	restored, err := service.Restore(ctx, pinned.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Configs["managed"].CodexACP.Env["TOKEN"]; got != oldSecret {
		t.Fatalf("retained revision TOKEN = %q, want old secret", got)
	}
	if _, err := service.Update(ctx, Change{ExpectedVersion: deleted.Version, ID: "managed", Config: codexConfig("stale")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale update error = %v, want ErrConflict", err)
	}
	if _, err := service.Delete(ctx, deleted.Version, "managed", usercmd.AuditEvent{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("stale delete error = %v, want ErrConflict", err)
	}
}

func TestCreateRejectsConfiguredAndActiveProviderCollisions(t *testing.T) {
	service := newTestService(t, map[string]agentconfig.Config{"configured": codexConfig("configured")}, &memoryStore{})
	if _, err := service.Create(t.Context(), Change{ID: "configured", Config: codexConfig("managed")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("configured collision error = %v, want ErrConflict", err)
	}
	state, err := service.Create(t.Context(), Change{ID: "managed", Config: codexConfig("first")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Create(t.Context(), Change{ExpectedVersion: state.Version, ID: "managed", Config: codexConfig("second")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("active collision error = %v, want ErrConflict", err)
	}
}

func TestRecreateDoesNotInheritAPIKey(t *testing.T) {
	store := &memoryStore{}
	service := newTestService(t, nil, store)
	oldAPIKey := "old-api-key"
	config := agentconfig.Config{Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{Model: "gpt"}}
	created, err := service.Create(t.Context(), Change{ID: "hosted", Config: config, Protected: ProtectedEdit{APIKey: &oldAPIKey}})
	if err != nil {
		t.Fatal(err)
	}
	deleted, err := service.Delete(t.Context(), created.Version, "hosted", usercmd.AuditEvent{})
	if err != nil {
		t.Fatal(err)
	}
	recreated, err := service.Create(t.Context(), Change{ExpectedVersion: deleted.Version, ID: "hosted", Config: config})
	if err != nil {
		t.Fatal(err)
	}
	record := findRecord(recreated.Records, "hosted")
	revision := findRevision(recreated.Revisions, "hosted", recordRevisionID(record))
	if revision == nil || revision.Definition.HasAPIKey || len(revision.ProtectedValues) != 0 {
		t.Fatalf("recreated revision inherited API key: %+v", revision)
	}
	compiled, err := service.CompileCurrent(t.Context(), "hosted")
	if err != nil {
		t.Fatal(err)
	}
	if got := compiled.Configs["hosted"].OpenAI.APIKey; got != "" {
		t.Fatalf("recreated API key = %q, want empty", got)
	}
}

func TestActiveConfiguredManagedCollisionIsInvalid(t *testing.T) {
	store := &memoryStore{state: State{
		Version: 1,
		Records: []Record{{ID: "duplicate", CurrentRevisionID: "revision", Enabled: true}},
		Revisions: []Revision{{ID: "revision", ProviderID: "duplicate", DriverType: agentconfig.AgentTypeCodexACP,
			Definition: Definition{Config: codexConfig("managed")}}},
	}}
	service := newTestService(t, map[string]agentconfig.Config{"duplicate": codexConfig("configured")}, store)
	for _, filter := range []InventoryFilter{InventoryActive, InventoryDeleted, InventoryAll} {
		if _, err := service.InventoryByFilter(t.Context(), filter); !errors.Is(err, ErrInvalid) {
			t.Fatalf("InventoryByFilter(%q) collision error = %v, want ErrInvalid", filter, err)
		}
	}
	if _, _, err := service.Get(t.Context(), "duplicate"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Get() collision error = %v, want ErrInvalid", err)
	}
	if _, err := service.Create(t.Context(), Change{ExpectedVersion: 1, ID: "duplicate", Config: codexConfig("replacement")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create() collision error = %v, want ErrConflict", err)
	}
}

func TestConfiguredProviderOwnsDeletedManagedReconciliationTombstone(t *testing.T) {
	store := &memoryStore{state: State{
		Version: 4,
		Records: []Record{{ID: "reserved", CurrentRevisionID: "old-revision", Deleted: true}},
		Revisions: []Revision{{ID: "old-revision", ProviderID: "reserved", DriverType: agentconfig.AgentTypeCodexACP,
			Definition: Definition{Config: codexConfig("old-managed")}}},
	}}
	service := newTestService(t, map[string]agentconfig.Config{"reserved": codexConfig("configured")}, store)

	active, err := service.InventoryByFilter(t.Context(), InventoryActive)
	if err != nil || len(active.Items) != 1 || active.Items[0].Source != SourceConfigured || active.Items[0].Record.Deleted {
		t.Fatalf("active reconciliation projection = %+v, %v", active, err)
	}
	deleted, err := service.InventoryByFilter(t.Context(), InventoryDeleted)
	if err != nil || len(deleted.Items) != 0 {
		t.Fatalf("deleted reconciliation projection = %+v, %v", deleted, err)
	}
	all, err := service.InventoryByFilter(t.Context(), InventoryAll)
	if err != nil || len(all.Items) != 1 || all.Items[0].Source != SourceConfigured || all.Items[0].Record.Deleted {
		t.Fatalf("all reconciliation projection = %+v, %v", all, err)
	}
	detail, version, err := service.Get(t.Context(), "reserved")
	if err != nil || version != store.state.Version || detail.Source != SourceConfigured || detail.Record.Deleted {
		t.Fatalf("reconciliation detail = %+v, version %d, %v", detail, version, err)
	}
	if _, err := service.Create(t.Context(), Change{ExpectedVersion: store.state.Version, ID: "reserved", Config: codexConfig("replacement")}); !errors.Is(err, ErrConflict) {
		t.Fatalf("Create() reused configured reservation: %v", err)
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
