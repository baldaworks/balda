package mcpmanage

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

type recordingDiagnosticProbe struct {
	revisions       []mcpcmd.Revision
	transientValues []mcpcmd.LaunchValues
	result          mcpcmd.DiagnosticResult
}

func (p *recordingDiagnosticProbe) DiagnoseMCP(_ context.Context, revision mcpcmd.Revision) mcpcmd.DiagnosticResult {
	p.revisions = append(p.revisions, revision)
	return p.result
}

func (p *recordingDiagnosticProbe) DiagnoseMCPWithValues(_ context.Context, revision mcpcmd.Revision, values mcpcmd.LaunchValues) mcpcmd.DiagnosticResult {
	p.revisions = append(p.revisions, revision)
	p.transientValues = append(p.transientValues, mcpcmd.LaunchValues{Env: maps.Clone(values.Env), Headers: maps.Clone(values.Headers)})
	return p.result
}

type forbiddenDiagnosticCatalog struct{}

func (forbiddenDiagnosticCatalog) PublishMCP(context.Context, func() error) error {
	panic("diagnostic published catalog state")
}
func (forbiddenDiagnosticCatalog) MCPHealth(context.Context, mcpcmd.Connection) (mcpcmd.Status, int, error) {
	panic("diagnostic read runtime health")
}
func (forbiddenDiagnosticCatalog) RetryMCPAuthorization(context.Context, mcpcmd.AuthBinding) error {
	panic("diagnostic retried authorization")
}

func TestDiagnoseCurrentUsesExactSelectedManagedRevisionWithoutWrites(t *testing.T) {
	s, store, _ := definitionHarness(t)
	created, err := s.Create(t.Context(), definitionCreate())
	if err != nil {
		t.Fatal(err)
	}
	stale := store.revisions[created.Connection.CurrentRevisionID]
	stale.ID = "stale-revision"
	stale.Definition.Command = "stale-worker"
	store.revisions[stale.ID] = stale
	probe := &recordingDiagnosticProbe{result: mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 3}}
	s.diagnostic = probe
	s.catalog = forbiddenDiagnosticCatalog{}
	writes := len(store.writes)

	result, err := s.DiagnoseCurrent(t.Context(), created.Connection.ID, definitionCreate().Authority)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != probe.result.Outcome || result.ToolCount != probe.result.ToolCount || result.CheckedAt.IsZero() || len(probe.revisions) != 1 || len(probe.transientValues) != 0 {
		t.Fatalf("diagnostic result/calls/transient calls = %+v/%d/%d", result, len(probe.revisions), len(probe.transientValues))
	}
	got := probe.revisions[0]
	if got.ID != created.Connection.CurrentRevisionID || got.Definition.Command != "worker-mcp" {
		t.Fatalf("diagnosed revision = %+v", got)
	}
	if len(store.writes) != writes {
		t.Fatal("managed diagnostic changed durable definitions")
	}
}

func TestDiagnoseCurrentRetainsLastResultForExactConfiguredRevision(t *testing.T) {
	s, _, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{
		Connection: mcpcmd.Connection{ID: "config:knowl", PublicID: "knowl", Source: mcpcmd.SourceConfig, Enabled: true},
		Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://knowl.example/mcp", ConfigRevision: "config-one"},
	}}
	probe := &recordingDiagnosticProbe{result: mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 2}}
	s.diagnostic = probe

	result, err := s.DiagnoseCurrent(t.Context(), "config:knowl", definitionCreate().Authority)
	if err != nil {
		t.Fatal(err)
	}
	if result.CheckedAt.IsZero() {
		t.Fatal("diagnostic result has no check time")
	}
	items, err := s.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 || items[0].LastDiagnostic == nil || *items[0].LastDiagnostic != result {
		t.Fatalf("retained diagnostic = %+v, want %+v", items, result)
	}
	if len(probe.revisions) != 1 {
		t.Fatalf("passive inventory reran diagnostic: %d calls", len(probe.revisions))
	}

	configured.items[0].Definition.ConfigRevision = "config-next"
	items, err = s.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if items[0].LastDiagnostic != nil {
		t.Fatalf("changed revision inherited diagnostic: %+v", items[0].LastDiagnostic)
	}

	fresh, err := NewDefinitions(s.credentials, s.store, s.configured, s.catalog, s.probe, s.diagnostic)
	if err != nil {
		t.Fatal(err)
	}
	items, err = fresh.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if items[0].LastDiagnostic != nil {
		t.Fatalf("fresh process inherited diagnostic: %+v", items[0].LastDiagnostic)
	}
}

func TestDiagnoseCurrentReloadsConfiguredPrivateValuesAtActionTime(t *testing.T) {
	s, store, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{
		Connection: mcpcmd.Connection{ID: "config:knowl", PublicID: "knowl", Source: mcpcmd.SourceConfig, Enabled: true},
		Definition: mcpcmd.Definition{
			Transport:      mcpcmd.TransportHTTP,
			URL:            "https://knowl.example/mcp",
			ConfigRevision: "config-one",
			Headers:        map[string]mcpcmd.ValueBinding{"Authorization": {Kind: mcpcmd.ValueProtected}},
		},
	}}
	configured.values = mcpcmd.LaunchValues{Headers: map[string]string{"Authorization": "Bearer first-private-value"}}
	probe := &recordingDiagnosticProbe{result: mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 1}}
	s.diagnostic = probe
	s.catalog = forbiddenDiagnosticCatalog{}

	if _, err := s.DiagnoseCurrent(t.Context(), "config:knowl", definitionCreate().Authority); err != nil {
		t.Fatal(err)
	}
	configured.items[0].Definition.URL = "https://current.example/mcp"
	configured.items[0].Definition.ConfigRevision = "config-two"
	configured.values.Headers["Authorization"] = "Bearer second-private-value"
	store.connections["stored-config"] = mcpcmd.Connection{ID: "stored-config", PublicID: "knowl", Source: mcpcmd.SourceConfig, CurrentRevisionID: "retained-capture"}
	probe.result = mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticFailed}
	result, err := s.DiagnoseCurrent(t.Context(), "stored-config", definitionCreate().Authority)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
		t.Fatalf("failed diagnostic result = %+v", result)
	}
	if len(probe.revisions) != 2 || len(probe.transientValues) != 2 || len(store.writes) != 0 || len(store.connections) != 1 || len(store.revisions) != 0 {
		t.Fatalf("configured diagnostic calls/values/writes = %d/%d/%d", len(probe.revisions), len(probe.transientValues), len(store.writes))
	}
	firstValues := probe.transientValues[0]
	secondValues := probe.transientValues[1]
	if probe.revisions[0].Definition.ConfigRevision != "config-one" || firstValues.Headers["Authorization"] != "Bearer first-private-value" {
		t.Fatal("first diagnostic did not use its action-time configuration")
	}
	if probe.revisions[1].Definition.ConfigRevision != "config-two" || probe.revisions[1].Definition.URL != "https://current.example/mcp" || secondValues.Headers["Authorization"] != "Bearer second-private-value" {
		t.Fatal("second diagnostic reused stale configured values")
	}
}

func TestDiagnoseCurrentUsesConfiguredStaticHeaderWithoutCredentialKey(t *testing.T) {
	for _, tc := range []struct {
		name         string
		connectionID string
		retain       bool
	}{
		{name: "configuration public ID", connectionID: "config:knowl"},
		{name: "retained configuration ID", connectionID: "retained-knowl", retain: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, store, configured := definitionHarness(t)
			withoutKey, err := New("")
			if err != nil {
				t.Fatal(err)
			}
			s.credentials = withoutKey
			configured.items = []mcpcmd.Item{{
				Connection: mcpcmd.Connection{ID: "config:knowl", PublicID: "knowl", Source: mcpcmd.SourceConfig, Enabled: true},
				Definition: mcpcmd.Definition{
					Transport:      mcpcmd.TransportHTTP,
					URL:            "https://knowl.example/mcp",
					ConfigRevision: "current-knowl",
					Headers:        map[string]mcpcmd.ValueBinding{"Authorization": {Kind: mcpcmd.ValueProtected}},
				},
			}}
			configured.values = mcpcmd.LaunchValues{Headers: map[string]string{"Authorization": "Bearer configured-only"}}
			if tc.retain {
				store.connections[tc.connectionID] = mcpcmd.Connection{ID: tc.connectionID, PublicID: "knowl", Source: mcpcmd.SourceConfig, CurrentRevisionID: "obsolete-capture"}
			}
			probe := &recordingDiagnosticProbe{result: mcpcmd.DiagnosticResult{Outcome: mcpcmd.DiagnosticSucceeded, ToolCount: 4}}
			s.diagnostic = probe
			s.catalog = forbiddenDiagnosticCatalog{}
			connectionsBefore := maps.Clone(store.connections)
			revisionsBefore := maps.Clone(store.revisions)
			grantsBefore := maps.Clone(store.grants)
			bindingsBefore := maps.Clone(configured.items[0].Definition.Headers)
			valuesBefore := maps.Clone(configured.values.Headers)

			result, err := s.DiagnoseCurrent(t.Context(), tc.connectionID, definitionCreate().Authority)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != probe.result.Outcome || result.ToolCount != probe.result.ToolCount || result.CheckedAt.IsZero() || len(probe.revisions) != 1 || len(probe.transientValues) != 1 {
				t.Fatalf("diagnostic result/calls = %+v/%d/%d", result, len(probe.revisions), len(probe.transientValues))
			}
			if got := probe.transientValues[0].Headers["Authorization"]; got != "Bearer configured-only" {
				t.Fatalf("diagnostic Authorization = %q", got)
			}
			if len(probe.revisions[0].ProtectedValues) != 0 {
				t.Fatal("configured diagnostic created an encrypted capture")
			}
			if len(store.writes) != 0 || !reflect.DeepEqual(store.connections, connectionsBefore) || !reflect.DeepEqual(store.revisions, revisionsBefore) || !reflect.DeepEqual(store.grants, grantsBefore) || !reflect.DeepEqual(configured.items[0].Definition.Headers, bindingsBefore) || !reflect.DeepEqual(configured.values.Headers, valuesBefore) {
				t.Fatal("configured diagnostic mutated configuration or durable MCP state")
			}
		})
	}
}

func TestDiagnoseCurrentChecksAuthorityBeforePrivateResolutionOrProbe(t *testing.T) {
	s, store, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "config:knowl", PublicID: "knowl", Source: mcpcmd.SourceConfig}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://knowl.example/mcp", ConfigRevision: "current"}}}
	probe := &recordingDiagnosticProbe{}
	s.diagnostic = probe
	store.writeError = mcpcmd.ErrForbidden

	_, err := s.DiagnoseCurrent(t.Context(), "config:knowl", definitionCreate().Authority)
	if !errors.Is(err, mcpcmd.ErrForbidden) || configured.authorizationCalls != 0 || len(probe.revisions) != 0 {
		t.Fatalf("unauthorized diagnostic = %v, private/probe calls = %d/%d", err, configured.authorizationCalls, len(probe.revisions))
	}
}

func TestDiagnoseCurrentRejectsMissingOrInvalidCurrentSelection(t *testing.T) {
	s, store, configured := definitionHarness(t)
	probe := &recordingDiagnosticProbe{}
	s.diagnostic = probe
	authority := definitionCreate().Authority

	if _, err := s.DiagnoseCurrent(t.Context(), "missing", authority); !errors.Is(err, mcpcmd.ErrNotFound) {
		t.Fatalf("missing connection error = %v", err)
	}
	store.connections["managed"] = mcpcmd.Connection{ID: "managed", PublicID: "worker", Source: mcpcmd.SourceManaged, CurrentRevisionID: "missing-revision"}
	if _, err := s.DiagnoseCurrent(t.Context(), "managed", authority); !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatalf("missing current revision error = %v", err)
	}
	configured.items = []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "config:invalid", PublicID: "invalid", Source: mcpcmd.SourceConfig}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "file:///private", ConfigRevision: "invalid"}}}
	if _, err := s.DiagnoseCurrent(t.Context(), "config:invalid", authority); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatalf("invalid configured definition error = %v", err)
	}
	if len(probe.revisions) != 0 || len(store.writes) != 0 {
		t.Fatal("invalid current selection reached probe or durable state")
	}
}
