package webhookbackofficeapp

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/google/uuid"
)

type testRouteStore struct {
	routes map[string]webhookroutecmd.Record
}

func (s *testRouteStore) Get(_ context.Context, name string) (webhookroutecmd.Record, bool, error) {
	r, ok := s.routes[name]
	return r, ok, nil
}
func (s *testRouteStore) LookupByPath(_ context.Context, path string) (webhookroutecmd.Record, bool, error) {
	for _, route := range s.routes {
		if route.Path == path {
			return route, true, nil
		}
	}
	return webhookroutecmd.Record{}, false, nil
}
func (s *testRouteStore) List(context.Context) ([]webhookroutecmd.Record, error)          { return nil, nil }
func (s *testRouteStore) ReconcileConfig(context.Context, []webhookroutecmd.Record) error { return nil }
func (s *testRouteStore) CheckAuthority(_ context.Context, a webhookroutecmd.Authority) error {
	if a.SessionID == "" {
		return webhookroutecmd.ErrForbidden
	}
	return nil
}
func (s *testRouteStore) Save(context.Context, webhookroutecmd.Mutation) error { return nil }

type testAcceptor struct{ requests []webhookcmd.Request }

func (a *testAcceptor) Accept(_ context.Context, request webhookcmd.Request) (webhookcmd.Result, error) {
	a.requests = append(a.requests, request)
	return webhookcmd.Result{JobID: "accepted-job"}, nil
}

func TestPostUsesCurrentRouteAndFormKey(t *testing.T) {
	store := &testRouteStore{routes: map[string]webhookroutecmd.Record{
		"configured": {Name: "configured", Source: webhookroutecmd.SourceConfig, Path: "/hooks/configured",
			PromptTemplate: "{{.RawBody}}", DedupeSource: webhookroutecmd.DedupeSourceBodySHA,
			Enabled: true, Version: 1},
		"disabled": {Name: "disabled", Source: webhookroutecmd.SourceManaged, Path: "/hooks/disabled",
			PromptTemplate: "{{.RawBody}}", Version: 2},
		"archived": {Name: "archived", Source: webhookroutecmd.SourceManaged, Path: "/hooks/archived",
			PromptTemplate: "{{.RawBody}}", Deleted: true, Version: 3},
	}}
	acceptor := &testAcceptor{}
	ingress, err := webhookapp.NewIngress([]webhookapp.ConfiguredRoute{{Name: "configured", Path: "/hooks/configured",
		PromptTemplate: "{{.RawBody}}", DedupeSource: webhookroutecmd.DedupeSourceBodySHA}}, store, acceptor)
	if err != nil {
		t.Fatal(err)
	}
	operations := New(webhookmanagement.New(store), ingress, nil)
	authority := webhookroutecmd.Authority{SessionID: "admin", At: time.Now().UTC()}
	key := uuid.NewString()
	request := webhookroutecmd.TestPost{Name: "configured", Body: "example", RequestKey: key,
		ExpectedVersion: 1, Authority: authority}
	if _, err := operations.TestPost(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	request.Body = "another body"
	if _, err := operations.TestPost(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	if len(acceptor.requests) != 2 || acceptor.requests[0].DedupeKey != acceptor.requests[1].DedupeKey ||
		!strings.HasPrefix(acceptor.requests[0].DedupeKey, "webhook-test:") || !acceptor.requests[0].Test {
		t.Fatalf("test request key was not stable: %+v", acceptor.requests)
	}
	request.Name, request.ExpectedVersion = "disabled", 2
	if _, err := operations.TestPost(t.Context(), request); err != webhookroutecmd.ErrInvalid {
		t.Fatalf("unconfirmed disabled route: %v", err)
	}
	request.ConfirmDisabled = true
	if _, err := operations.TestPost(t.Context(), request); err != nil {
		t.Fatalf("confirmed disabled route: %v", err)
	}
	request.Name, request.ExpectedVersion = "archived", 3
	if _, err := operations.TestPost(t.Context(), request); err != webhookroutecmd.ErrNotFound {
		t.Fatalf("archived route: %v", err)
	}
	request.Name, request.ExpectedVersion = "configured", 2
	if _, err := operations.TestPost(t.Context(), request); err != webhookroutecmd.ErrConflict {
		t.Fatalf("stale route version: %v", err)
	}
	request.ExpectedVersion, request.Authority.SessionID = 1, ""
	if _, err := operations.TestPost(t.Context(), request); err != webhookroutecmd.ErrForbidden {
		t.Fatalf("stale administrator authority: %v", err)
	}
	if len(acceptor.requests) != 3 {
		t.Fatalf("rejected requests reached admission: %d", len(acceptor.requests))
	}
}

func TestProjectHistoryKeepsOpaqueInputExact(t *testing.T) {
	raw := string([]byte{0xff, 0x00, '<'})
	item := projectHistory(state.WebhookHistoryRecord{RawBody: &raw, Source: webhookcmd.SourceTest,
		JobID: "test-job", JobStatus: "succeeded"})
	if !item.InputAvailable || item.Input != "Hexadecimal bytes: ff003c" || item.Source != webhookcmd.SourceTest {
		t.Fatalf("opaque input projection = %+v", item)
	}
	old := projectHistory(state.WebhookHistoryRecord{JobID: "old-job"})
	if old.InputAvailable || old.Input != "" {
		t.Fatalf("legacy input projection = %+v", old)
	}
}
