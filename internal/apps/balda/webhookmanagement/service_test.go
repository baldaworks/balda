package webhookmanagement

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

type memoryStore struct {
	routes       map[string]webhookroutecmd.Record
	mutations    []webhookroutecmd.Mutation
	reconciled   []webhookroutecmd.Record
	authorityErr error
}

const (
	legacyManagedPath = "/old/orders"
	legacyEditedPath  = "/events-v2"
)

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
	return webhookroutecmd.Definition{Name: "events", Path: "/events",
		PromptTemplate: "{{ .RawBody }}", ReportTo: "main_chat", DedupeSource: webhookroutecmd.DedupeSourceBodySHA}
}

func TestManagedPathIsDerivedAndPostedOverrideRejected(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store, ManagedPaths{Prefix: "/balda/webhooks"})
	definition := testDefinition()
	definition.Name, definition.Path = "orders", ""
	created, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: definition, Authority: testAuthority()})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := created.Item.Definition.Path, "/balda/webhooks/orders"; got != want {
		t.Fatalf("created path = %q, want %q", got, want)
	}
	if got := store.routes["orders"].Path; got != "/balda/webhooks/orders" {
		t.Fatalf("stored path = %q", got)
	}
	definition.Name, definition.Path = "other", "/orders"
	if _, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: definition, Authority: testAuthority()}); !errors.Is(err, webhookroutecmd.ErrInvalid) {
		t.Fatalf("divergent posted path error = %v, want invalid", err)
	}
	if _, found := store.routes["other"]; found {
		t.Fatal("divergent path created a route")
	}
}

func TestLegacyConstructorAcceptsSubmittedFormPaths(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	definition := testDefinition()
	definition.Path = " /events "
	created, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: definition, Authority: testAuthority()})
	if err != nil || created.Item.Definition.Path != "/events" {
		t.Fatalf("legacy create = %+v, %v", created.Item, err)
	}
	definition.Path = legacyEditedPath
	updated, err := service.Update(t.Context(), webhookroutecmd.Update{Name: "events", ExpectedVersion: 1,
		Definition: definition, Authority: testAuthority()})
	if err != nil || updated.Definition.Path != legacyEditedPath {
		t.Fatalf("legacy update = %+v, %v", updated, err)
	}
}

func TestManagedLegacyPathIsPreservedOnUpdate(t *testing.T) {
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddManagedWebhook("legacy", legacyManagedPath, http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{routes: map[string]webhookroutecmd.Record{
		"legacy": {Name: "legacy", Path: legacyManagedPath, Source: webhookroutecmd.SourceManaged,
			Enabled: true, Version: 4, PromptTemplate: "{{.RawBody}}"},
	}}
	service := New(store, ManagedPaths{Prefix: "/balda/webhooks", Ownership: registry})
	definition := testDefinition()
	definition.Name, definition.Path, definition.PromptTemplate = "legacy", "", "updated {{.RawBody}}"
	updated, err := service.Update(t.Context(), webhookroutecmd.Update{Name: "legacy", ExpectedVersion: 4,
		Definition: definition, Authority: testAuthority()})
	if err != nil {
		t.Fatal(err)
	}
	if got := updated.Definition.Path; got != legacyManagedPath {
		t.Fatalf("updated path = %q, want retained legacy path", got)
	}
	definition.Path = "/balda/webhooks/legacy"
	if _, err := service.Update(t.Context(), webhookroutecmd.Update{Name: "legacy", ExpectedVersion: 5,
		Definition: definition, Authority: testAuthority()}); !errors.Is(err, webhookroutecmd.ErrInvalid) {
		t.Fatalf("divergent update path error = %v, want invalid", err)
	}
	if got := store.routes["legacy"]; got.Path != legacyManagedPath || got.Version != 5 {
		t.Fatalf("rejected update changed row: %+v", got)
	}
}

func TestManagedLegacyPathConflictLeavesRowUnchanged(t *testing.T) {
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddBackoffice("browser", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddGateway("slack legacy alias", "/old/slack", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	if err := registry.AddWebhook("config webhook legacy", "/old/config", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ name, path, owner string }{
		{"browser", "/balda/backoffice/hidden", "browser"},
		{"gateway", "/balda/gateway/slack/events", "gateway"},
		{"gateway alias", "/old/slack", "slack legacy alias"},
		{"config webhook", "/old/config", "config webhook legacy"},
	} {
		t.Run(test.name, func(t *testing.T) {
			store := &memoryStore{routes: map[string]webhookroutecmd.Record{
				"legacy": {Name: "legacy", Path: test.path, Source: webhookroutecmd.SourceManaged,
					Version: 3, PromptTemplate: "{{.RawBody}}"},
			}}
			service := New(store, ManagedPaths{Prefix: "/balda/webhooks", Ownership: registry})
			definition := testDefinition()
			definition.Name, definition.Path = "legacy", ""
			_, err := service.Update(t.Context(), webhookroutecmd.Update{Name: "legacy", ExpectedVersion: 3,
				Definition: definition, Authority: testAuthority()})
			if !errors.Is(err, webhookroutecmd.ErrConflict) || !strings.Contains(err.Error(), test.path) ||
				!strings.Contains(err.Error(), test.owner) {
				t.Fatalf("update error = %v, want path and owner conflict", err)
			}
			if len(store.mutations) != 0 || store.routes["legacy"].Version != 3 {
				t.Fatal("conflicting update changed stored row")
			}
			_, err = service.SetEnabled(t.Context(), webhookroutecmd.ChangeSelection{Name: "legacy",
				ExpectedVersion: 3, Enabled: true, Authority: testAuthority()})
			if !errors.Is(err, webhookroutecmd.ErrConflict) || !strings.Contains(err.Error(), test.path) ||
				!strings.Contains(err.Error(), test.owner) {
				t.Fatalf("enable error = %v, want path and owner conflict", err)
			}
			if len(store.mutations) != 0 || store.routes["legacy"].Enabled {
				t.Fatal("conflicting enable changed stored row")
			}
		})
	}
}

func TestManagedCreateConflictsWithActiveConfigPath(t *testing.T) {
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.AddWebhook("config webhook orders", "/balda/webhooks/orders", http.NotFoundHandler()); err != nil {
		t.Fatal(err)
	}
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store, ManagedPaths{Prefix: "/balda/webhooks", Ownership: registry})
	definition := testDefinition()
	definition.Name, definition.Path = "orders", ""
	_, err = service.Create(t.Context(), webhookroutecmd.Create{Definition: definition, Authority: testAuthority()})
	if !errors.Is(err, webhookroutecmd.ErrConflict) || !strings.Contains(err.Error(), "/balda/webhooks/orders") ||
		!strings.Contains(err.Error(), "config webhook orders") {
		t.Fatalf("create error = %v, want path and config owner", err)
	}
	if len(store.mutations) != 0 || len(store.routes) != 0 {
		t.Fatal("conflicting create changed stored rows")
	}
}

func TestManagedLegacyPathBecomesLiveAfterEnable(t *testing.T) {
	store := &memoryStore{routes: map[string]webhookroutecmd.Record{
		"legacy": {Name: "legacy", Path: legacyManagedPath, Source: webhookroutecmd.SourceManaged,
			Version: 2, PromptTemplate: "{{.RawBody}}"},
	}}
	registry, err := httpfx.NewRegistry("/balda")
	if err != nil {
		t.Fatal(err)
	}
	if err := registry.SetWebhookLookup("generic webhooks", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	}), func(_ context.Context, path string) (bool, error) {
		route := store.routes["legacy"]
		return route.Enabled && route.Path == path, nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := registry.Handler()
	status := func() int {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, legacyManagedPath, nil))
		return recorder.Code
	}
	if got := status(); got != http.StatusNotFound {
		t.Fatalf("disabled route status = %d, want 404", got)
	}
	service := New(store, ManagedPaths{Prefix: "/balda/webhooks", Ownership: registry})
	item, err := service.SetEnabled(t.Context(), webhookroutecmd.ChangeSelection{Name: "legacy",
		ExpectedVersion: 2, Enabled: true, Authority: testAuthority()})
	if err != nil || !item.Enabled || item.Definition.Path != legacyManagedPath {
		t.Fatalf("enable = %+v, %v", item, err)
	}
	if got := status(); got != http.StatusAccepted {
		t.Errorf("enabled route status = %d, want 202", got)
	}
}

func TestManagedPathCanBeReclaimedAfterDisableOrDelete(t *testing.T) {
	for _, action := range []string{"disable", "delete"} {
		t.Run(action, func(t *testing.T) {
			const reclaimedPath = "/balda/webhooks/new"
			registry, err := httpfx.NewRegistry("/balda")
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.AddManagedWebhook("old", reclaimedPath, http.NotFoundHandler()); err != nil {
				t.Fatal(err)
			}
			store := &memoryStore{routes: map[string]webhookroutecmd.Record{
				"old": {Name: "old", Path: reclaimedPath, Source: webhookroutecmd.SourceManaged,
					Enabled: true, Version: 1, PromptTemplate: "{{.RawBody}}"},
			}}
			service := New(store, ManagedPaths{Prefix: "/balda/webhooks", Ownership: registry})
			if action == "disable" {
				_, err = service.SetEnabled(t.Context(), webhookroutecmd.ChangeSelection{Name: "old", ExpectedVersion: 1,
					Enabled: false, Authority: testAuthority()})
			} else {
				_, err = service.Delete(t.Context(), webhookroutecmd.Delete{Name: "old", ExpectedVersion: 1,
					Authority: testAuthority()})
			}
			if err != nil {
				t.Fatal(err)
			}
			definition := testDefinition()
			definition.Name, definition.Path = "new", ""
			created, err := service.Create(t.Context(), webhookroutecmd.Create{Definition: definition, Authority: testAuthority()})
			if err != nil || created.Item.Definition.Path != reclaimedPath {
				t.Fatalf("reclaim = %+v, %v", created.Item, err)
			}
		})
	}
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
	definition.Path = legacyEditedPath
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
		{"missing path", func(d *webhookroutecmd.Definition) { d.Path = "" }},
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
		Name: "configured", Path: "/configured", PromptTemplate: "{{.RawBody}}",
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

func TestDisabledPlaceholderConfigDoesNotBlockReconciliation(t *testing.T) {
	store := &memoryStore{routes: make(map[string]webhookroutecmd.Record)}
	service := New(store)
	placeholder := webhookroutecmd.ConfiguredRoute{Name: "later", Enabled: false}
	valid := webhookroutecmd.ConfiguredRoute{Name: "configured", Path: "/configured",
		PromptTemplate: "{{.RawBody}}", Enabled: false}
	if err := service.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{placeholder, valid}); err != nil {
		t.Fatalf("disabled route reconciliation = %v", err)
	}
	if len(store.reconciled) != 1 || store.reconciled[0].Name != "configured" || store.reconciled[0].Enabled {
		t.Fatalf("reconciled routes = %+v", store.reconciled)
	}
	placeholder.Enabled = true
	if err := service.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{placeholder}); !errors.Is(err, webhookroutecmd.ErrInvalid) {
		t.Fatalf("enabled placeholder = %v, want invalid", err)
	}
}

func TestConfigOwnedRouteIsReadOnly(t *testing.T) {
	store := &memoryStore{routes: map[string]webhookroutecmd.Record{
		"configured": {Name: "configured", Source: webhookroutecmd.SourceConfig,
			Path: "/configured", PromptTemplate: "{{.RawBody}}", Enabled: true, Version: 1},
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
