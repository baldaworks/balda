package mcpmanage

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type committedReadFailureStore struct {
	*definitionMemoryStore
	committed bool
}

func (s *committedReadFailureStore) SaveDefinition(ctx context.Context, mutation Mutation) error {
	if err := s.definitionMemoryStore.SaveDefinition(ctx, mutation); err != nil {
		return err
	}
	s.committed = true
	return nil
}

func (s *committedReadFailureStore) GetMCPConnection(ctx context.Context, id string) (mcpcmd.Connection, bool, error) {
	if s.committed {
		return mcpcmd.Connection{}, false, mcpcmd.ErrUnavailable
	}
	return s.definitionMemoryStore.GetMCPConnection(ctx, id)
}

func TestCommittedCreationRetainsIdentityWhenReadbackFails(t *testing.T) {
	s, store, _ := definitionHarness(t)
	s.store = &committedReadFailureStore{definitionMemoryStore: store}
	item, err := s.CreateForAuthorization(t.Context(), mcpcmd.CreateDefinition{PublicID: "worker", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://mcp.example.org/tools", Targets: mcpcmd.Targets{All: true}}, Authority: definitionCreate().Authority}, nil)
	if !errors.Is(err, mcpcmd.ErrUnavailable) || item.Connection.ID == "" || item.Status != mcpcmd.StatusPending {
		t.Fatalf("committed creation lost saved identity: %v", err)
	}
	saved := store.connections[item.Connection.ID]
	if len(store.connections) != 1 || item.Connection.CurrentRevisionID != saved.CurrentRevisionID || item.Connection.Version != saved.Version || !item.Definition.OAuth {
		t.Fatal("fallback metadata does not describe the committed revision")
	}
}

func TestConfiguredScopeChangeRetainsHistoricalBindingOnly(t *testing.T) {
	s, store, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "config:file-tools", PublicID: "file-tools", Source: mcpcmd.SourceConfig, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://file.example.org/tools", ConfigRevision: "file-v1"}}}
	request := mcpcmd.PrepareAuthorization{ConnectionID: "config:file-tools", Scopes: []string{"tools:read"}, Authority: definitionCreate().Authority}
	first, err := s.PrepareAuthorization(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	binding := mcpcmd.AuthBinding{ConnectionID: first.ConnectionID, Resource: first.Definition.URL, Issuer: "https://issuer.example.org", ClientID: "client"}
	first.Definition.AuthBinding = &binding
	store.revisions[first.ID] = first
	request.Scopes = []string{}
	next, err := s.PrepareAuthorization(t.Context(), request)
	if err != nil || next.ID == first.ID || next.Definition.AuthBinding != nil || len(next.Definition.Scopes) != 0 {
		t.Fatal("changed configured scopes inherited the current binding")
	}
	old := store.revisions[first.ID]
	if old.Definition.AuthBinding == nil || *old.Definition.AuthBinding != binding || !slices.Equal(old.Definition.Scopes, []string{"tools:read"}) {
		t.Fatal("configured scope change rewrote historical binding identity")
	}
}

func TestManagedAuthorizationPreparesImmutableRevision(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition = mcpcmd.Definition{Transport: transport, URL: "https://mcp.example.org/tools", Targets: mcpcmd.Targets{All: true}}
			request.Values.Headers = map[string]mcpcmd.ValueEdit{"X-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "retained-header"}}
			created, err := s.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			original := store.revisions[created.Connection.CurrentRevisionID]
			prepare := mcpcmd.PrepareAuthorization{ConnectionID: created.Connection.ID, Scopes: []string{"tools:read"}, Authority: request.Authority}
			first, err := s.PrepareAuthorization(t.Context(), prepare)
			if err != nil {
				t.Fatalf("explicit authorization of static remote failed: %v", err)
			}
			if first.ID == original.ID || !first.Definition.OAuth || !slices.Equal(first.Definition.Scopes, []string{"tools:read"}) || first.Definition.AuthBinding != nil {
				t.Fatal("authorization did not prepare a distinct OAuth revision")
			}
			for _, r := range []mcpcmd.Revision{original, first} {
				values, err := s.credentials.ResolveValues(r)
				if err != nil || values.Headers["X-Secret"] != "retained-header" {
					t.Fatal("authorization lost exact protected headers")
				}
			}
			binding := mcpcmd.AuthBinding{ConnectionID: first.ConnectionID, Resource: first.Definition.URL, Issuer: "https://issuer.example.org", ClientID: "client"}
			first.Definition.AuthBinding = &binding
			store.revisions[first.ID] = first
			for _, scopes := range [][]string{nil, {"tools:read"}} {
				prepare.Scopes = scopes
				again, err := s.PrepareAuthorization(t.Context(), prepare)
				if err != nil || again.ID != first.ID || again.Definition.AuthBinding == nil || *again.Definition.AuthBinding != binding || len(store.writes) != 2 {
					t.Fatal("unchanged authorization rewrote current revision or binding")
				}
			}
			prepare.Scopes = []string{}
			cleared, err := s.PrepareAuthorization(t.Context(), prepare)
			if err != nil || cleared.ID == first.ID || len(cleared.Definition.Scopes) != 0 || cleared.Definition.AuthBinding != nil {
				t.Fatal("explicit empty scopes did not prepare an unbound new revision")
			}
			retained := store.revisions[original.ID]
			if retained.Definition.OAuth || !bytes.Equal(retained.ProtectedValues, original.ProtectedValues) || store.revisions[first.ID].Definition.AuthBinding == nil {
				t.Fatal("authorization preparation rewrote a historical revision")
			}
		})
	}
}

func TestManagedAuthorizationRejectsConflictingOrUnauthorizedPreparation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		header    bool
		scopes    []string
		forbidden bool
		want      error
	}{
		{name: "static authorization", header: true, want: mcpcmd.ErrConflict},
		{name: "invalid scopes", scopes: []string{"invalid scope"}, want: mcpcmd.ErrInvalid},
		{name: "revoked authority", forbidden: true, want: mcpcmd.ErrForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition = mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://mcp.example.org/tools", Targets: mcpcmd.Targets{All: true}}
			if tc.header {
				request.Values.Headers = map[string]mcpcmd.ValueEdit{"authorization": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "static-token"}}
			}
			created, err := s.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if tc.forbidden {
				store.writeError = mcpcmd.ErrForbidden
			}
			_, err = s.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: created.Connection.ID, Scopes: tc.scopes, Authority: request.Authority})
			if !errors.Is(err, tc.want) || len(store.writes) != 1 || store.connections[created.Connection.ID].CurrentRevisionID != created.Connection.CurrentRevisionID {
				t.Fatalf("invalid preparation changed current state: %v", err)
			}
		})
	}
}

func TestConnectionEditAndProbeKeepTrustedOAuthSettings(t *testing.T) {
	for _, probe := range []bool{false, true} {
		name := "save"
		if probe {
			name = "probe"
		}
		t.Run(name, func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition = mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://mcp.example.org/tools", OAuth: true, Scopes: []string{"tools:read"}, Targets: mcpcmd.Targets{All: true}}
			created, err := s.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			previous := store.revisions[created.Connection.CurrentRevisionID]
			binding := mcpcmd.AuthBinding{ConnectionID: previous.ConnectionID, Resource: previous.Definition.URL, Issuer: "https://issuer.example.org", ClientID: "client"}
			previous.Definition.AuthBinding = &binding
			store.revisions[previous.ID] = previous
			update := mcpcmd.UpdateDefinition{ConnectionID: created.Connection.ID, ExpectedVersion: created.Connection.Version, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: previous.Definition.URL, Targets: mcpcmd.Targets{Providers: []string{"hosted"}}}, Enabled: true, Authority: request.Authority}
			var item mcpcmd.Item
			if probe {
				item, err = s.ProbeUpdate(t.Context(), update)
			} else {
				item, err = s.Update(t.Context(), update)
			}
			if err != nil || !item.Definition.OAuth || !slices.Equal(item.Definition.Scopes, []string{"tools:read"}) || item.Definition.AuthBinding == nil || *item.Definition.AuthBinding != binding {
				t.Fatalf("ordinary connection edit lost trusted authorization: %v", err)
			}
			if probe && (len(store.writes) != 1 || store.connections[created.Connection.ID].CurrentRevisionID != previous.ID) {
				t.Fatal("probe published a candidate revision")
			}
		})
	}
}
