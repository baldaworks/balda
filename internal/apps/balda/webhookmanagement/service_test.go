package webhookmanagement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type memoryStore struct {
	routes       map[string]webhookroutecmd.Record
	mutations    []webhookroutecmd.Mutation
	reconciled   []webhookroutecmd.Record
	authorityErr error
}

func (s *memoryStore) Get(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	r, ok := s.routes[name]
	r.SecretVerifier = ""
	return r, ok, nil
}

func (s *memoryStore) List(context.Context) ([]webhookroutecmd.Record, error) {
	var routes []webhookroutecmd.Record
	for _, r := range s.routes {
		if r.Deleted {
			continue
		}
		r.SecretVerifier = ""
		routes = append(routes, r)
	}
	return routes, nil
}

func (s *memoryStore) ReconcileConfig(_ context.Context, routes []webhookroutecmd.Record) error {
	s.reconciled = routes
	return nil
}

func (s *memoryStore) CheckAuthority(context.Context, webhookroutecmd.Authority) error {
	return s.authorityErr
}

func (s *memoryStore) Save(_ context.Context, m webhookroutecmd.Mutation) error {
	if r, ok := s.routes[m.Record.Name]; ok {
		if r.Source != webhookroutecmd.SourceManaged {
			return webhookroutecmd.ErrForbidden
		}
		if m.Kind == webhookroutecmd.MutationCreate || r.Version != m.ExpectedVersion {
			return webhookroutecmd.ErrConflict
		}
	} else if m.Kind != webhookroutecmd.MutationCreate {
		return webhookroutecmd.ErrNotFound
	}
	s.mutations = append(s.mutations, m)
	s.routes[m.Record.Name] = m.Record
	return nil
}

func testAuthority() webhookroutecmd.Authority {
	return webhookroutecmd.Authority{UserID: "admin", UserVersion: 1,
		CredentialVersion: 1, MFAVersion: 1, SessionID: "browser",
		SessionVersion: 1, At: time.Now().UTC()}
}

func testDefinition() webhookroutecmd.Definition {
	return webhookroutecmd.Definition{Name: "events",
		PromptTemplate: "{{ .RawBody }}", ReportTo: "main_chat", DedupeSource: webhookroutecmd.DedupeSourceBodySHA}
}

func TestManagedRouteLifecycleAndOneTimeSecret(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	authority := testAuthority()
	created, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: testDefinition(), Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	if created.Secret == "" || strings.Contains(created.Secret, "=") || created.Item.Version != 1 {
		t.Fatalf("created result = %+v", created.Item)
	}
	digest := sha256.Sum256([]byte(created.Secret))
	if got, want := store.mutations[0].Record.SecretVerifier, hex.EncodeToString(digest[:]); got != want {
		t.Fatalf("stored verifier = %q, want %q", got, want)
	}
	if strings.Contains(store.mutations[0].Audit.Reason, created.Secret) ||
		store.mutations[0].Audit.TargetID != "events" {
		t.Fatal("audit includes secret or wrong target")
	}
	if created.Item.ReportToKind != "managed_alias" || created.Item.Definition.ReportTo != "main_chat" {
		t.Fatalf("report target = %+v", created.Item)
	}
	item, err := service.Get(t.Context(), "events", authority)
	if err != nil || item.Version != 1 {
		t.Fatalf("GET = %+v, %v", item, err)
	}
	definition := testDefinition()
	definition.ReportTo = "telegram:-1003953132277:0"
	updated, err := service.Update(t.Context(), webhookroutecmd.Update{Name: "events", ExpectedVersion: 1,
		Definition: definition, Authority: authority})
	if err != nil || updated.Version != 2 || updated.ReportToKind != "locator" {
		t.Fatalf("update = %+v, %v", updated, err)
	}
	rotated, err := service.Rotate(t.Context(), webhookroutecmd.Rotate{Name: "events", ExpectedVersion: 2, Authority: authority})
	if err != nil || rotated.Secret == created.Secret || rotated.Item.Version != 3 {
		t.Fatalf("rotate = %+v, %v", rotated.Item, err)
	}
	if got := store.routes["events"].SecretVerifier; got == hex.EncodeToString(digest[:]) {
		t.Fatal("rotation retained old verifier")
	}
	if _, err := service.Rotate(t.Context(), webhookroutecmd.Rotate{Name: "events", ExpectedVersion: 2,
		Authority: authority}); !errors.Is(err, webhookroutecmd.ErrConflict) {
		t.Fatalf("stale rotation = %v", err)
	}
	disabled, err := service.SetEnabled(t.Context(), webhookroutecmd.ChangeSelection{Name: "events",
		ExpectedVersion: 3, Enabled: false, Authority: authority})
	if err != nil || disabled.Enabled || disabled.Version != 4 {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	archived, err := service.Delete(t.Context(), webhookroutecmd.Delete{Name: "events", ExpectedVersion: 4,
		Authority: authority})
	if err != nil || !archived.Deleted || archived.Enabled || archived.Version != 5 {
		t.Fatalf("delete = %+v, %v", archived, err)
	}
	if got, err := service.Get(t.Context(), "events", authority); err != nil || !got.Deleted {
		t.Fatalf("archived GET = %+v, %v", got, err)
	}
}

func TestManagedRouteValidationAndAuthority(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	authority := testAuthority()
	for _, tc := range []struct {
		name   string
		change func(*webhookroutecmd.Definition)
	}{
		{"invalid name", func(d *webhookroutecmd.Definition) { d.Name = "a/b" }},
		{"invalid template", func(d *webhookroutecmd.Definition) { d.PromptTemplate = "{{" }},
		{"ack without report", func(d *webhookroutecmd.Definition) { d.ReportTo, d.AckOnDelivery = "", true }},
		{"invalid report", func(d *webhookroutecmd.Definition) { d.ReportTo = "not a locator" }},
		{"secret dedupe", func(d *webhookroutecmd.Definition) {
			d.DedupeSource, d.DedupeHeader = webhookroutecmd.DedupeSourceHeader, webhookroutecmd.ManagedSecretHeader
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition := testDefinition()
			tc.change(&definition)
			if _, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: definition,
				Authority: authority}); !errors.Is(err, webhookroutecmd.ErrInvalid) {
				t.Fatalf("create = %v, want invalid", err)
			}
		})
	}
	if len(store.mutations) != 0 {
		t.Fatalf("invalid inputs mutated store %d times", len(store.mutations))
	}
	store.authorityErr = webhookroutecmd.ErrForbidden
	if _, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: testDefinition(),
		Authority: authority}); !errors.Is(err, webhookroutecmd.ErrForbidden) {
		t.Fatalf("forbidden create = %v", err)
	}
	if _, err := service.Inventory(t.Context(), authority); !errors.Is(err, webhookroutecmd.ErrForbidden) {
		t.Fatalf("forbidden inventory = %v", err)
	}
}

func TestConfiguredRouteReconciliationRetainsDisabledDeclarations(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	err := service.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{{
		Name: "configured", PromptTemplate: "{{.RawBody}}",
		ReportToKind: "alias", ReportToKey: "owner", AuthType: webhookroutecmd.AuthTypeHeader,
		AuthHeader: "X-Signature", Enabled: false,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.reconciled) != 1 || store.reconciled[0].Enabled ||
		store.reconciled[0].ReportToKind != "alias" || store.reconciled[0].SecretVerifier != "" {
		t.Fatalf("reconciled = %+v", store.reconciled)
	}
}

func TestConfigOwnedRouteIsReadOnly(t *testing.T) {
	store := &memoryStore{routes: map[string]webhookroutecmd.Record{
		"configured": {Name: "configured", Source: webhookroutecmd.SourceConfig,
			PromptTemplate: "{{.RawBody}}", Enabled: true, Version: 1},
	}}
	service := New(store)
	authority := testAuthority()
	item, err := service.Get(t.Context(), "configured", authority)
	if err != nil || item.Source != webhookroutecmd.SourceConfig {
		t.Fatalf("config route detail = %+v, %v", item, err)
	}
	if _, err := service.SetEnabled(t.Context(), webhookroutecmd.ChangeSelection{
		Name: "configured", ExpectedVersion: 1, Enabled: false, Authority: authority,
	}); !errors.Is(err, webhookroutecmd.ErrForbidden) {
		t.Fatalf("config selection = %v, want forbidden", err)
	}
	if len(store.mutations) != 0 {
		t.Fatalf("config route mutated %d times", len(store.mutations))
	}
}

func TestManagedNameIsImmutable(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	authority := testAuthority()
	created, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: testDefinition(), Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	definition := testDefinition()
	definition.Name = "renamed"
	_, err = service.Update(t.Context(), webhookroutecmd.Update{Name: "events", ExpectedVersion: created.Item.Version, Definition: definition, Authority: authority})
	if !errors.Is(err, webhookroutecmd.ErrInvalid) {
		t.Fatalf("rename = %v, want invalid", err)
	}
	if len(store.mutations) != 1 || store.routes["events"].Version != 1 {
		t.Fatal("rename mutated original route")
	}
}

func TestDisabledConfigRequiresValidDefinition(t *testing.T) {
	for _, route := range []webhookroutecmd.ConfiguredRoute{
		{Name: "invalid/slug", PromptTemplate: "{{.RawBody}}"},
		{Name: "orders", PromptTemplate: "{{"},
		{Name: "orders"},
	} {
		store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
		err := New(store).ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{route})
		if !errors.Is(err, webhookroutecmd.ErrInvalid) || len(store.reconciled) != 0 {
			t.Fatalf("invalid disabled config = %v, reconciled=%v", err, store.reconciled)
		}
	}
}
