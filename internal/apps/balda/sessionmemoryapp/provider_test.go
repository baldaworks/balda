package sessionmemoryapp

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestNativeProviderProcessesAndSearchesDerivedMemory(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`{"output":[{"category":"fact","text":"native memory","relation":"new"}]}`)}
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	provider, err := NewNativeProvider(sessionmemorytest.NewStore(), deriver, invoker)
	if err != nil {
		t.Fatalf("NewNativeProvider() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "native:personal:topic:1", Kind: sessionmemory.ScopeKindPersonal}
	turn, err := sessionmemory.NewTurn(scope, sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC), "remember", "native memory")
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	if err := provider.SyncTurn(context.Background(), turn); err != nil {
		t.Fatalf("SyncTurn() error = %v", err)
	}
	response, err := provider.SearchDerived(context.Background(), sessionmemory.DerivedSearchRequest{Scope: scope, Query: "native", Limit: 10})
	if err != nil {
		t.Fatalf("SearchDerived() error = %v", err)
	}
	if len(response.Results) != 1 || response.Results[0].Text != "native memory" || response.Results[0].Scope.Key != scope.Key {
		t.Fatalf("SearchDerived() = %#v", response)
	}
	if err := provider.Close(context.Background()); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestNativeProviderForgetsOneScopeAndPreservesAnother(t *testing.T) {
	invoker := &scriptedStructuredInvoker{output: []byte(`{"output":[{"category":"fact","text":"native memory","relation":"new"}]}`)}
	deriver, err := NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	provider, err := NewNativeProvider(sessionmemorytest.NewStore(), deriver, invoker)
	if err != nil {
		t.Fatalf("NewNativeProvider() error = %v", err)
	}
	firstScope := sessionmemory.Scope{Key: "native:personal:topic:forget", Kind: sessionmemory.ScopeKindPersonal}
	secondScope := sessionmemory.Scope{Key: "native:group:topic:keep", Kind: sessionmemory.ScopeKindGroup}
	first, err := sessionmemory.NewTurn(firstScope, sessionmemory.SessionRef{SessionID: "session-first", AgentSessionID: "agent-first"}, "turn-first", time.Date(2026, 8, 3, 16, 0, 0, 0, time.UTC), "forget me", "native memory")
	if err != nil {
		t.Fatalf("NewTurn(first) error = %v", err)
	}
	second, err := sessionmemory.NewTurn(secondScope, sessionmemory.SessionRef{SessionID: "session-second", AgentSessionID: "agent-second"}, "turn-second", time.Date(2026, 8, 3, 16, 1, 0, 0, time.UTC), "keep me", "native memory")
	if err != nil {
		t.Fatalf("NewTurn(second) error = %v", err)
	}
	ctx := context.Background()
	if err := provider.SyncTurn(ctx, first); err != nil {
		t.Fatalf("SyncTurn(first) error = %v", err)
	}
	if err := provider.SyncTurn(ctx, second); err != nil {
		t.Fatalf("SyncTurn(second) error = %v", err)
	}
	firstSearch, err := provider.SearchDerived(ctx, sessionmemory.DerivedSearchRequest{Scope: firstScope, Query: "native", Limit: 10})
	if err != nil || len(firstSearch.Results) != 1 {
		t.Fatalf("SearchDerived(first) = %#v, error = %v", firstSearch, err)
	}
	root := sessionmemory.RevisionRef{ItemID: firstSearch.Results[0].ItemID, RevisionID: firstSearch.Results[0].RevisionID}
	source := firstSearch.Results[0].Provenance.RawSources[0]
	outcome, err := provider.ForgetSource(ctx, sessionmemory.ForgetSourceCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Source:        source,
		ForgottenAt:   time.Date(2026, 8, 3, 17, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	if len(outcome.Sources) != 1 || len(outcome.Revisions) == 0 {
		t.Fatalf("ForgetSource() outcome = %#v", outcome)
	}
	replayed, err := provider.ForgetSource(ctx, sessionmemory.ForgetSourceCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Source:        source,
		ForgottenAt:   time.Date(2026, 8, 3, 18, 0, 0, 0, time.UTC),
	})
	if err != nil || replayed.OperationID != outcome.OperationID {
		t.Fatalf("ForgetSource() replay = %#v, error = %v", replayed, err)
	}
	afterForget, err := provider.SearchDerived(ctx, sessionmemory.DerivedSearchRequest{Scope: firstScope, Query: "native", Limit: 10})
	if err != nil {
		t.Fatalf("SearchDerived(first after forget) error = %v", err)
	}
	if len(afterForget.Results) != 0 {
		t.Fatalf("SearchDerived(first after forget) = %#v, want no readable results", afterForget)
	}
	traceErr := error(nil)
	if _, err := provider.Trace(ctx, sessionmemory.TraceRequest{Scope: firstScope, Root: root, MaxNodes: 10}); err != nil {
		traceErr = err
	}
	code, _, ok := sessionmemory.ClassifyError(traceErr)
	if !ok || code != sessionmemory.CodeForgotten {
		t.Fatalf("Trace(first after forget) error = %v, want %s", traceErr, sessionmemory.CodeForgotten)
	}
	secondSearch, err := provider.SearchDerived(ctx, sessionmemory.DerivedSearchRequest{Scope: secondScope, Query: "native", Limit: 10})
	if err != nil || len(secondSearch.Results) != 1 {
		t.Fatalf("SearchDerived(second after first forget) = %#v, error = %v", secondSearch, err)
	}
}
