package mcpmanage

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestCreateValidatesBeforeSavingAndSeparatesSavedFromReady(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportStdio, mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition.Transport = transport
			if transport != mcpcmd.TransportStdio {
				request.Definition.Command = ""
				request.Definition.URL = "https://mcp.example.org/tools"
			}
			item, err := s.Create(t.Context(), request)
			if err != nil {
				t.Fatalf("valid definition rejected: %v", err)
			}
			if len(store.connections) != 1 || len(store.revisions) != 1 || item.Connection.Source != mcpcmd.SourceManaged || item.Status != mcpcmd.StatusPending {
				t.Fatalf("saved definition misreported: %+v", item)
			}
		})
	}
	for _, tc := range []struct {
		name string
		edit func(*mcpcmd.CreateDefinition)
	}{
		{"missing command", func(r *mcpcmd.CreateDefinition) { r.Definition.Command = "" }},
		{"invalid argv", func(r *mcpcmd.CreateDefinition) { r.Definition.Args = []string{"bad\x00arg"} }},
		{"mixed transport", func(r *mcpcmd.CreateDefinition) { r.Definition.URL = "https://example.org" }},
		{"invalid URL", func(r *mcpcmd.CreateDefinition) {
			r.Definition.Transport = mcpcmd.TransportHTTP
			r.Definition.Command = ""
			r.Definition.URL = "ftp://example.org"
		}},
		{"URL credentials", func(r *mcpcmd.CreateDefinition) {
			r.Definition.Transport = mcpcmd.TransportHTTP
			r.Definition.Command = ""
			r.Definition.URL = "https://user:secret@example.org"
		}},
		{"empty selection", func(r *mcpcmd.CreateDefinition) { r.Definition.Targets = mcpcmd.Targets{} }},
		{"unknown provider", func(r *mcpcmd.CreateDefinition) {
			r.Definition.Targets = mcpcmd.Targets{Providers: []string{"unknown"}}
		}},
		{"ambiguous selection", func(r *mcpcmd.CreateDefinition) {
			r.Definition.Targets = mcpcmd.Targets{All: true, Providers: []string{"hosted"}}
		}},
		{"header injection", func(r *mcpcmd.CreateDefinition) {
			r.Definition.Transport = mcpcmd.TransportHTTP
			r.Definition.Command = ""
			r.Definition.URL = "https://example.org"
			r.Values.Headers = map[string]mcpcmd.ValueEdit{"X-Key": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "secret\r\nInjected: yes"}}
		}},
		{"reserved ID", func(r *mcpcmd.CreateDefinition) { r.PublicID = "balda" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			tc.edit(&request)
			if _, err := s.Create(t.Context(), request); !errors.Is(err, mcpcmd.ErrInvalid) {
				t.Fatalf("invalid definition accepted: %v", err)
			}
			if len(store.connections) != 0 {
				t.Fatal("invalid definition reached durable commit")
			}
		})
	}
}

type definitionMemoryStore struct {
	connections map[string]mcpcmd.Connection
	revisions   map[string]mcpcmd.Revision
	writes      []Mutation
	writeError  error
}

func (s *definitionMemoryStore) CheckMCPAuthority(_ context.Context, a mcpcmd.Authority) error {
	if s.writeError != nil {
		return s.writeError
	}
	if a.UserID == "" || a.SessionID == "" {
		return mcpcmd.ErrForbidden
	}
	return nil
}

func (s *definitionMemoryStore) SaveDefinition(_ context.Context, m Mutation) error {
	if s.writeError != nil {
		return s.writeError
	}
	old, found := s.connections[m.Connection.ID]
	if (m.ExpectedVersion == 0 && found) || (m.ExpectedVersion != 0 && (!found || old.Version != m.ExpectedVersion)) {
		return mcpcmd.ErrConflict
	}
	for _, c := range s.connections {
		if c.ID != m.Connection.ID && c.PublicID == m.Connection.PublicID {
			return mcpcmd.ErrConflict
		}
	}
	c := m.Connection
	c.Version = m.ExpectedVersion + 1
	c.PublishedVersion = old.PublishedVersion
	s.connections[c.ID] = c
	if m.Revision != nil {
		s.revisions[m.Revision.ID] = *m.Revision
	}
	s.writes = append(s.writes, m)
	return nil
}
func (s *definitionMemoryStore) GetMCPConnection(_ context.Context, id string) (mcpcmd.Connection, bool, error) {
	c, ok := s.connections[id]
	return c, ok, nil
}
func (s *definitionMemoryStore) ListMCPConnections(context.Context) ([]mcpcmd.Connection, error) {
	var out []mcpcmd.Connection
	for _, c := range s.connections {
		out = append(out, c)
	}
	return out, nil
}
func (s *definitionMemoryStore) GetMCPRevision(_ context.Context, connection, id string) (mcpcmd.Revision, bool, error) {
	r, ok := s.revisions[id]
	return r, ok && r.ConnectionID == connection, nil
}

type definitionConfigured struct {
	items     []mcpcmd.Item
	providers []string
}

func (c *definitionConfigured) MCPDefinitions(context.Context) ([]mcpcmd.Item, error) {
	return c.items, nil
}
func (c *definitionConfigured) ProviderIDs(context.Context) ([]string, error) {
	return c.providers, nil
}

type definitionCatalog struct {
	status       mcpcmd.Status
	publishError error
}

func (c *definitionCatalog) PublishMCP(_ context.Context, commit func() error) error {
	if err := commit(); err != nil {
		return err
	}
	return c.publishError
}
func (c *definitionCatalog) MCPHealth(context.Context, mcpcmd.Connection) (mcpcmd.Status, int, error) {
	return c.status, 2, nil
}

type definitionProbe struct{}

func (definitionProbe) ProbeMCP(context.Context, mcpcmd.Revision) (int, error) { return 2, nil }
func definitionHarness(t *testing.T) (*Definitions, *definitionMemoryStore, *definitionConfigured) {
	t.Helper()
	credentials, err := New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	store := &definitionMemoryStore{connections: make(map[string]mcpcmd.Connection), revisions: make(map[string]mcpcmd.Revision)}
	configured := &definitionConfigured{providers: []string{"hosted", "acp"}}
	s, err := NewDefinitions(credentials, store, configured, &definitionCatalog{status: mcpcmd.StatusReady}, definitionProbe{})
	if err != nil {
		t.Fatal(err)
	}
	return s, store, configured
}
func definitionCreate() mcpcmd.CreateDefinition {
	return mcpcmd.CreateDefinition{PublicID: "worker-tools", Enabled: true, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportStdio, Command: "worker-mcp", Targets: mcpcmd.Targets{All: true}}, Authority: mcpcmd.Authority{UserID: "admin", UserVersion: 1, CredentialVersion: 1, SessionID: "browser", SessionVersion: 1, At: time.Now().UTC(), FreshProofAge: time.Minute}}
}

func TestHybridInventoryConflictsAndConfiguredReadOnly(t *testing.T) {
	s, store, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "config:file-tools", PublicID: "file-tools", Source: mcpcmd.SourceConfig, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://file.example.org"}}}
	r := definitionCreate()
	r.PublicID = "file-tools"
	if _, err := s.Create(t.Context(), r); !errors.Is(err, mcpcmd.ErrConflict) || len(store.writes) != 0 {
		t.Fatalf("config ID shadowed: %v", err)
	}
	if _, err := s.Update(t.Context(), mcpcmd.UpdateDefinition{ConnectionID: "config:file-tools", ExpectedVersion: 1}); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("configured entry editable: %v", err)
	}
	if _, err := s.Delete(t.Context(), mcpcmd.ChangeSelection{ConnectionID: "config:file-tools", ExpectedVersion: 1}); !errors.Is(err, mcpcmd.ErrForbidden) {
		t.Fatalf("configured entry deletable: %v", err)
	}
	r = definitionCreate()
	created, err := s.Create(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Inventory(t.Context())
	if err != nil || len(items) != 2 {
		t.Fatalf("hybrid inventory: %v, %v", items, err)
	}
	if _, err := s.Create(t.Context(), r); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("managed ID reused: %v", err)
	}
	// A deployment file added after a managed save is an explicit conflict.
	configured.items = append(configured.items, mcpcmd.Item{Connection: mcpcmd.Connection{ID: "config:worker-tools", PublicID: created.Connection.PublicID, Source: mcpcmd.SourceConfig, Enabled: true}})
	items, err = s.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	conflicts := 0
	for _, item := range items {
		if item.Connection.PublicID == created.Connection.PublicID && item.Status == mcpcmd.StatusConflict {
			conflicts++
		}
	}
	if conflicts != 2 {
		t.Fatalf("conflicting sources silently selected: %+v", items)
	}
}

func TestDefinitionEditsFenceVersionsPreserveSecretsAndRetainPins(t *testing.T) {
	s, store, _ := definitionHarness(t)
	request := definitionCreate()
	const secret = "definition-secret-fixture"
	request.Values.Env = map[string]mcpcmd.ValueEdit{"KEY": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: secret}}
	created, err := s.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	oldRevision := created.Connection.CurrentRevisionID
	update := mcpcmd.UpdateDefinition{ConnectionID: created.Connection.ID, ExpectedVersion: 1, Definition: created.Definition, Enabled: true, Authority: request.Authority}
	update.Definition.Targets = mcpcmd.Targets{Providers: []string{"acp"}}
	updated, err := s.Update(t.Context(), update)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Connection.Version != 2 || updated.Connection.PublicID != created.Connection.PublicID || updated.Connection.CurrentRevisionID == oldRevision {
		t.Fatal("identity/revision/version changed incorrectly")
	}
	for _, id := range []string{oldRevision, updated.Connection.CurrentRevisionID} {
		values, err := s.credentials.ResolveValues(store.revisions[id])
		if err != nil || values.Env["KEY"] != secret {
			t.Fatalf("exact retained secret unavailable: %v", err)
		}
	}
	data, err := json.Marshal(updated)
	if err != nil || bytes.Contains(data, []byte(secret)) {
		t.Fatal("secret entered public inventory")
	}
	if _, err := s.Update(t.Context(), update); !errors.Is(err, mcpcmd.ErrConflict) || len(store.writes) != 2 {
		t.Fatalf("stale update committed: %v", err)
	}
	store.writeError = mcpcmd.ErrForbidden
	update.ExpectedVersion = 2
	if _, err := s.Update(t.Context(), update); !errors.Is(err, mcpcmd.ErrForbidden) || len(store.writes) != 2 {
		t.Fatalf("authority rejection changed state: %v", err)
	}
	store.writeError = nil
	selection := mcpcmd.ChangeSelection{ConnectionID: created.Connection.ID, ExpectedVersion: 2, Authority: request.Authority}
	disabled, err := s.SetEnabled(t.Context(), selection)
	if err != nil || disabled.Status != mcpcmd.StatusDisabled {
		t.Fatalf("disable: %+v, %v", disabled, err)
	}
	selection.ExpectedVersion = 3
	deleted, err := s.Delete(t.Context(), selection)
	if err != nil || deleted.Status != mcpcmd.StatusDeleted || len(store.revisions) != 2 {
		t.Fatalf("delete lost retained pin: %+v, %v", deleted, err)
	}
	selection.ExpectedVersion = 4
	selection.Enabled = true
	if _, err := s.SetEnabled(t.Context(), selection); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("tombstone resurrected: %v", err)
	}
}

func TestProbeAndPublicationErrorsDoNotExposeCredentialsOrClaimReadiness(t *testing.T) {
	s, store, _ := definitionHarness(t)
	request := definitionCreate()
	s.probe = failingDefinitionProbe{}
	item, err := s.Probe(t.Context(), request)
	if !errors.Is(err, mcpcmd.ErrUnavailable) || item.Status != mcpcmd.StatusUnavailable || len(store.writes) != 0 {
		t.Fatalf("failed probe reported ready or saved: %+v, %v", item, err)
	}
	if err.Error() != mcpcmd.ErrUnavailable.Error() {
		t.Fatal("probe error leaked raw body")
	}
	s.probe = definitionProbe{}
	item, err = s.Probe(t.Context(), request)
	if err != nil || item.Status != mcpcmd.StatusPending || item.ToolCount != 2 || len(store.writes) != 0 {
		t.Fatalf("candidate probe altered selection: %+v, %v", item, err)
	}
	s.catalog = &definitionCatalog{status: mcpcmd.StatusReady, publishError: errors.New("upstream token-secret-fixture")}
	item, err = s.Create(t.Context(), request)
	if err != nil || item.Status != mcpcmd.StatusPending || len(store.writes) != 1 {
		t.Fatalf("failed publish lost durable/pending state: %+v, %v", item, err)
	}
	c := store.connections[item.Connection.ID]
	c.PublishedVersion = c.Version
	store.connections[c.ID] = c
	items, err := s.Inventory(t.Context())
	if err != nil || items[0].Status != mcpcmd.StatusReady {
		t.Fatalf("published healthy revision not ready: %v", err)
	}
	s.catalog = &definitionCatalog{status: mcpcmd.StatusUnavailable}
	items, err = s.Inventory(t.Context())
	if err != nil || items[0].Status != mcpcmd.StatusUnavailable {
		t.Fatalf("failed runtime reported ready: %v", err)
	}
}

type failingDefinitionProbe struct{}

func (failingDefinitionProbe) ProbeMCP(context.Context, mcpcmd.Revision) (int, error) {
	return 0, errors.New("issuer body contains token-secret-fixture")
}

func TestReenableRevalidatesCurrentProviderSelection(t *testing.T) {
	s, store, configured := definitionHarness(t)
	request := definitionCreate()
	request.Definition.Targets = mcpcmd.Targets{Providers: []string{"acp"}}
	created, err := s.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	selection := mcpcmd.ChangeSelection{ConnectionID: created.Connection.ID, ExpectedVersion: 1, Authority: request.Authority}
	if _, err := s.SetEnabled(t.Context(), selection); err != nil {
		t.Fatal(err)
	}
	configured.providers = []string{"hosted"}
	selection.ExpectedVersion = 2
	selection.Enabled = true
	if _, err := s.SetEnabled(t.Context(), selection); !errors.Is(err, mcpcmd.ErrInvalid) || len(store.writes) != 2 {
		t.Fatalf("unknown provider selected on reenable: %v", err)
	}
}

func TestProbeRejectsMissingAndRevokedAuthorityBeforeLaunching(t *testing.T) {
	s, store, _ := definitionHarness(t)
	probe := &countingDefinitionProbe{}
	s.probe = probe
	request := definitionCreate()
	request.Authority = mcpcmd.Authority{}
	if _, err := s.Probe(t.Context(), request); !errors.Is(err, mcpcmd.ErrForbidden) || probe.calls != 0 {
		t.Fatalf("anonymous probe executed: %v, %d calls", err, probe.calls)
	}
	request = definitionCreate()
	store.writeError = mcpcmd.ErrForbidden
	if _, err := s.Probe(t.Context(), request); !errors.Is(err, mcpcmd.ErrForbidden) || probe.calls != 0 {
		t.Fatalf("revoked browser probe executed: %v, %d calls", err, probe.calls)
	}
}

type countingDefinitionProbe struct{ calls int }

func (p *countingDefinitionProbe) ProbeMCP(context.Context, mcpcmd.Revision) (int, error) {
	p.calls++
	return 0, nil
}
