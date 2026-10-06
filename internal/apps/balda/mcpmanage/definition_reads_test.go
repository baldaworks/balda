package mcpmanage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

func TestWorkerAuthorizationReportsCurrentSafeGrantSeparately(t *testing.T) {
	for _, status := range []mcpcmd.GrantStatus{mcpcmd.GrantAuthorized, mcpcmd.GrantAuthRequired, mcpcmd.GrantDisconnected} {
		t.Run(string(status), func(t *testing.T) {
			s, store, _ := definitionHarness(t)
			request := definitionCreate()
			request.Definition = mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", OAuth: true, Targets: mcpcmd.Targets{All: true}}
			item, err := s.Create(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := s.WorkerAuthorization(t.Context(), item); err != nil || got != mcpcmd.GrantAuthRequired {
				t.Fatalf("unbound = %s/%v", got, err)
			}
			binding := mcpcmd.AuthBinding{ConnectionID: item.Connection.ID, Resource: item.Definition.URL, Issuer: "https://issuer.example", ClientID: "worker"}
			grant := mcpcmd.Grant{ID: "grant", Binding: binding, Generation: 1, Status: status, TokenEndpointAuthMethod: mcpcmd.ClientAuthNone}
			secrets := GrantSecrets{}
			if status == mcpcmd.GrantAuthorized {
				secrets = GrantSecrets{AccessToken: "private-authorization-token", TokenType: "Bearer"}
			}
			grant.ProtectedValues, err = s.credentials.ProtectGrant(grant, secrets)
			if err != nil {
				t.Fatal(err)
			}
			store.grants = map[mcpcmd.AuthBinding]mcpcmd.Grant{binding: grant}
			if status == mcpcmd.GrantDisconnected {
				pending := grant
				pending.Status = mcpcmd.GrantAuthRequired
				store.grants[binding] = pending
			}
			item, err = s.BindAuthorization(t.Context(), mcpcmd.SelectAuthorization{ConnectionID: item.Connection.ID, ExpectedRevisionID: item.Connection.CurrentRevisionID, Binding: binding, Authority: request.Authority})
			if err != nil {
				t.Fatal(err)
			}
			store.grants[binding] = grant
			got, err := s.WorkerAuthorization(t.Context(), item)
			if err != nil || got != status || item.Status == mcpcmd.StatusReady {
				t.Fatalf("grant/readiness = %s/%s/%v", got, item.Status, err)
			}
			if status == mcpcmd.GrantAuthorized {
				grant.ProtectedValues[len(grant.ProtectedValues)-1] ^= 1
				store.grants[binding] = grant
				if got, err := s.WorkerAuthorization(t.Context(), item); got != "" || !errors.Is(err, mcpcmd.ErrCredentials) {
					t.Fatal("corrupt grant was displayed as authorized")
				}
			}
			stale := item
			stale.Connection.CurrentRevisionID += "-missing"
			if got, err := s.WorkerAuthorization(t.Context(), stale); got != "" || !errors.Is(err, mcpcmd.ErrConflict) {
				t.Fatal("historical identity inherited current grant")
			}
		})
	}
}

func TestWorkerAuthorizationDoesNotBorrowDifferentConfiguredCapture(t *testing.T) {
	s, _, configured := definitionHarness(t)
	configured.items = []mcpcmd.Item{{Connection: mcpcmd.Connection{ID: "config:file-tools", PublicID: "file-tools", Source: mcpcmd.SourceConfig, Enabled: true}, Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp", ConfigRevision: "file-one"}}}
	capture, err := s.PrepareAuthorization(t.Context(), mcpcmd.PrepareAuthorization{ConnectionID: "config:file-tools", Authority: definitionCreate().Authority})
	if err != nil {
		t.Fatal(err)
	}
	items, err := s.Inventory(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if status, err := s.WorkerAuthorization(t.Context(), items[0]); err != nil || status != mcpcmd.GrantAuthRequired {
		t.Fatalf("current capture = %s/%v", status, err)
	}
	for _, current := range []mcpcmd.Definition{{Transport: mcpcmd.TransportHTTP, URL: "https://changed.example/mcp", ConfigRevision: "file-two"}, {Transport: mcpcmd.TransportStdio, Command: "current-stdio", ConfigRevision: "file-stdio"}} {
		configured.items[0].Definition = current
		items, err = s.Inventory(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		if status, err := s.WorkerAuthorization(t.Context(), items[0]); err != nil || status != "" {
			t.Fatalf("different capture inherited %s/%v from %s", status, err, capture.ID)
		}
	}
}

type candidateProbe struct {
	credentials *Service
	values      []string
}

func (p *candidateProbe) ProbeMCP(_ context.Context, r mcpcmd.Revision) (int, error) {
	values, err := p.credentials.ResolveValues(r)
	if err != nil {
		return 0, err
	}
	p.values = append(p.values, values.Env["TOKEN"])
	return 3, nil
}

func TestProbeUpdateKeepsProtectedValuesWithoutSavingOrPublishing(t *testing.T) {
	s, store, _ := definitionHarness(t)
	probe := &candidateProbe{credentials: s.credentials}
	s.probe = probe
	request := definitionCreate()
	request.Values.Env = map[string]mcpcmd.ValueEdit{"TOKEN": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "retained-private-value"}}
	item, err := s.Create(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	original := store.revisions[item.Connection.CurrentRevisionID]
	update := mcpcmd.UpdateDefinition{ConnectionID: item.Connection.ID, ExpectedVersion: item.Connection.Version, Definition: item.Definition, Enabled: true, Authority: request.Authority}
	for _, edit := range []mcpcmd.ValueEdit{{Operation: mcpcmd.ValueKeep}, {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "candidate-private-value"}, {Operation: mcpcmd.ValueRemove}} {
		update.Values.Env = map[string]mcpcmd.ValueEdit{"TOKEN": edit}
		candidate, err := s.ProbeUpdate(t.Context(), update)
		if err != nil || candidate.Status != mcpcmd.StatusPending || candidate.ToolCount != 3 {
			t.Fatalf("probe = %+v/%v", candidate, err)
		}
		encoded, _ := json.Marshal(candidate)
		if bytes.Contains(encoded, []byte("private-value")) {
			t.Fatal("probe leaked candidate or retained protected value")
		}
		if len(store.writes) != 1 || len(store.revisions) != 1 || store.connections[item.Connection.ID] != item.Connection || !bytes.Equal(store.revisions[original.ID].ProtectedValues, original.ProtectedValues) {
			t.Fatal("probe changed durable revision/selection")
		}
	}
	if len(probe.values) != 3 || probe.values[0] != "retained-private-value" || probe.values[1] != "candidate-private-value" || probe.values[2] != "" {
		t.Fatalf("candidate values = %q", probe.values)
	}
	update.ExpectedVersion++
	if _, err := s.ProbeUpdate(t.Context(), update); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatal("stale candidate launched")
	}
	update.ExpectedVersion = item.Connection.Version
	update.Authority = mcpcmd.Authority{At: time.Now()}
	if _, err := s.ProbeUpdate(t.Context(), update); !errors.Is(err, mcpcmd.ErrForbidden) || len(probe.values) != 3 {
		t.Fatal("unauthorized candidate launched")
	}
	providers, err := s.ProviderIDs(t.Context())
	if err != nil || len(providers) != 2 {
		t.Fatalf("provider choices = %v/%v", providers, err)
	}
}
