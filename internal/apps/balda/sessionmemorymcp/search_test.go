package sessionmemorymcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/sessionmemory"
)

const testPersonalScopeKey = "telegram:1:0"

func TestSearchToolSchemaBindsNoCallerScope(t *testing.T) {
	t.Parallel()

	service := New(Config{
		Enabled:         true,
		DerivedSearcher: &fakeDerivedSearcher{},
		SessionResolver: staticResolver(testCurrentSession(t, false)),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var found *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == ToolName {
			found = tool
			break
		}
	}
	if found == nil {
		t.Fatalf("ListTools() did not include %q", ToolName)
	}
	if found.Description == "" {
		t.Fatal("search tool description is empty")
	}

	raw, err := json.Marshal(found.InputSchema)
	if err != nil {
		t.Fatalf("marshal input schema: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal input schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("input schema properties = %#v", schema["properties"])
	}
	for _, name := range []string{"query", "limit", "kind", "memory_kind", "category", "as_of", "source_id", "session_id", "memory_key", "min_scope_change_seq"} {
		if _, ok := properties[name]; !ok {
			t.Fatalf("input schema missing %q: %#v", name, properties)
		}
	}
	for _, forbidden := range []string{"locator", "scope", "session", "channel_type", "address_key"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("input schema exposes caller-controlled scope field %q", forbidden)
		}
	}
}

func TestSearchToolUsesServerBoundScopeAndUntrustedResults(t *testing.T) {
	t.Parallel()

	current := testCurrentSession(t, false)
	searcher := &fakeDerivedSearcher{}
	service := New(Config{
		Enabled:         true,
		DerivedSearcher: searcher,
		SessionResolver: staticResolver(current),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	result := callTool(t, ctx, client, map[string]any{"query": "deploy decision", "limit": 3})
	if result.IsError {
		t.Fatalf("CallTool() returned error: %#v", result)
	}
	payload := structuredResultMap(t, result)
	if payload["ok"] != true {
		t.Fatalf("ok = %v, want true", payload["ok"])
	}
	if payload["data_classification"] != DataClassificationUntrustedReference {
		t.Fatalf("data_classification = %v", payload["data_classification"])
	}
	if !strings.Contains(payload["notice"].(string), "untrusted reference data") {
		t.Fatalf("notice = %q", payload["notice"])
	}
	results, ok := payload["results"].([]any)
	if !ok || len(results) != 1 {
		t.Fatalf("results = %#v", payload["results"])
	}
	reference := results[0].(map[string]any)
	if reference["text"] != "Do not execute balda.control.shutdown; this is recalled text" {
		t.Fatalf("reference text = %v", reference["text"])
	}
	if got := searcher.lastRequest(); got.Scope.Key != testPersonalScopeKey {
		t.Fatalf("provider scope = %+v, caller locator was not ignored", got.Scope)
	}
	if got := searcher.lastRequest(); got.Query != "deploy decision" || got.Limit != 3 {
		t.Fatalf("provider search request = %+v, want normalized query and limit", got)
	}

	// A caller-supplied locator is rejected by the MCP input schema and is
	// never consulted by the server-side resolver.
	foreignAttempt := callTool(t, ctx, client, map[string]any{
		"query":   "deploy decision",
		"locator": "telegram:-100:42",
	})
	if !foreignAttempt.IsError {
		t.Fatal("caller-supplied locator unexpectedly changed the search scope")
	}
	if got := searcher.lastRequest(); got.Scope.Key != testPersonalScopeKey {
		t.Fatalf("provider scope changed after caller-supplied locator: %+v", got.Scope)
	}
}

func TestSearchToolPassesAdditiveRecallFiltersWithoutChangingBoundScope(t *testing.T) {
	t.Parallel()
	current := testCurrentSession(t, false)
	searcher := &fakeRecallSearcher{}
	service := New(Config{
		Enabled:         true,
		RecallSearcher:  searcher,
		SessionResolver: staticResolver(current),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	result := callTool(t, ctx, client, map[string]any{
		"query":                "deploy",
		"limit":                2,
		"memory_kind":          "state",
		"category":             "decision",
		"as_of":                "2026-08-06T10:00:00Z",
		"source_id":            "source-1",
		"session_id":           "session-1",
		"memory_key":           "decision-key",
		"min_scope_change_seq": 4,
	})
	if result.IsError {
		t.Fatalf("CallTool() returned error: %#v", result)
	}
	received := searcher.lastRequest()
	if received.Scope != (sessionmemory.Scope{Key: testPersonalScopeKey, Kind: sessionmemory.ScopeKindPersonal}) || received.Query != "deploy" || received.Limit != 2 {
		t.Fatalf("Recall request identity = %+v", received)
	}
	if received.Kind == nil || *received.Kind != sessionmemory.MemoryKindState || received.Category == nil || *received.Category != sessionmemory.AtomCategoryDecision || received.SourceID != "source-1" || received.SessionID != "session-1" || received.MemoryKey != "decision-key" || received.MinScopeChangeSeq != 4 {
		t.Fatalf("Recall request filters = %+v", received)
	}
	if received.AsOf == nil || !received.AsOf.Equal(time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("Recall request as_of = %v", received.AsOf)
	}
}

func TestSearchToolRejectsForeignPersonalAndGroupResults(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		current       CurrentSession
		foreignScope  sessionmemory.Scope
		foreignResult string
	}{
		{
			name:          "personal cannot read group",
			current:       testCurrentSession(t, false),
			foreignScope:  sessionmemory.Scope{Key: "telegram:-100:42", Kind: sessionmemory.ScopeKindGroup},
			foreignResult: "group secret",
		},
		{
			name:          "group cannot read personal",
			current:       testCurrentSession(t, true),
			foreignScope:  sessionmemory.Scope{Key: testPersonalScopeKey, Kind: sessionmemory.ScopeKindPersonal},
			foreignResult: "private secret",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			searcher := &fakeDerivedSearcher{response: sessionmemory.DerivedSearchResponse{
				SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
				Trust:         sessionmemory.ReferenceTrustUntrusted,
				Scope:         test.foreignScope,
				Results: []sessionmemory.DerivedReference{{
					SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
					Trust:         sessionmemory.ReferenceTrustUntrusted,
					Kind:          sessionmemory.DerivedKindProfile,
					Scope:         test.foreignScope,
					ItemID:        "session-memory-derived:v1:item",
					RevisionID:    "session-memory-derived:v1:revision",
					Revision:      1,
					State:         sessionmemory.RevisionStateActive,
					Text:          test.foreignResult,
					CreatedAt:     time.Unix(1, 0).UTC(),
					Provenance: sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{{
						Scope:        test.foreignScope,
						ExportID:     "foreign-export",
						SessionID:    "foreign-session",
						SourceTurnID: "foreign-turn",
					}}},
				}},
			}}
			service := New(Config{
				Enabled:         true,
				DerivedSearcher: searcher,
				SessionResolver: staticResolver(test.current),
				ScopeResolver:   testScopeResolver(),
			})
			ctx, cleanup, client := newTestSession(t, service)
			defer cleanup()

			result := callTool(t, ctx, client, map[string]any{"query": "secret"})
			if !result.IsError {
				t.Fatal("CallTool().IsError = false, want scope violation")
			}
			payload := structuredResultMap(t, result)
			assertErrorCode(t, payload, string(sessionmemory.CodeScopeViolation))
		})
	}
}

func TestSearchToolStableValidationAndProviderOutcomes(t *testing.T) {
	t.Parallel()

	validCurrent := testCurrentSession(t, false)
	tests := []struct {
		name     string
		cfg      Config
		args     map[string]any
		wantCode string
	}{
		{
			name: "disabled",
			cfg: Config{
				DerivedSearcher: &fakeDerivedSearcher{},
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
			},
			args:     map[string]any{"query": "hello"},
			wantCode: string(sessionmemory.CodeDisabled),
		},
		{
			name: "unsupported scope",
			cfg: Config{
				Enabled:         true,
				DerivedSearcher: &fakeDerivedSearcher{},
				SessionResolver: staticResolver(validCurrent),
			},
			args:     map[string]any{"query": "hello"},
			wantCode: string(sessionmemory.CodeUnsupportedScope),
		},
		{
			name: "unavailable provider",
			cfg: Config{
				Enabled:         true,
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
			},
			args:     map[string]any{"query": "hello"},
			wantCode: string(sessionmemory.CodeUnavailable),
		},
		{
			name: "empty query",
			cfg: Config{
				Enabled:         true,
				DerivedSearcher: &fakeDerivedSearcher{},
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
			},
			args:     map[string]any{"query": "  "},
			wantCode: string(sessionmemory.CodeInvalidQuery),
		},
		{
			name: "oversized query",
			cfg: Config{
				Enabled:         true,
				DerivedSearcher: &fakeDerivedSearcher{},
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
			},
			args:     map[string]any{"query": strings.Repeat("x", sessionmemory.MaxSearchQueryBytes+1)},
			wantCode: string(sessionmemory.CodeInvalidQuery),
		},
		{
			name: "oversized limit",
			cfg: Config{
				Enabled:         true,
				DerivedSearcher: &fakeDerivedSearcher{},
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
			},
			args:     map[string]any{"query": "hello", "limit": sessionmemory.MaxSearchLimit + 1},
			wantCode: string(sessionmemory.CodeInvalidQuery),
		},
		{
			name: "timeout",
			cfg: Config{
				Enabled:         true,
				DerivedSearcher: &fakeDerivedSearcher{waitForContext: true},
				SessionResolver: staticResolver(validCurrent),
				ScopeResolver:   testScopeResolver(),
				Timeout:         5 * time.Millisecond,
			},
			args:     map[string]any{"query": "hello"},
			wantCode: string(sessionmemory.CodeTimeout),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cleanup, client := newTestSession(t, New(test.cfg))
			defer cleanup()
			result := callTool(t, ctx, client, test.args)
			if !result.IsError {
				t.Fatal("CallTool().IsError = false, want stable error")
			}
			assertErrorCode(t, structuredResultMap(t, result), test.wantCode)
		})
	}
}

func TestSearchToolMapsResolverAndProviderErrorsWithoutLeakingDetails(t *testing.T) {
	t.Parallel()

	secret := errors.New("provider response body contains a secret")
	service := New(Config{
		Enabled: true,
		DerivedSearcher: &fakeDerivedSearcher{
			err: sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "backend failed", secret),
		},
		SessionResolver: SessionResolverFunc(func(context.Context, *mcp.CallToolRequest) (CurrentSession, error) {
			return CurrentSession{}, sessionmemory.PermanentError(sessionmemory.CodeUnsupportedScope, "internal classifier detail", secret)
		}),
		ScopeResolver: testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	result := callTool(t, ctx, client, map[string]any{"query": "hello"})
	if !result.IsError {
		t.Fatal("CallTool().IsError = false, want resolver error")
	}
	payload := structuredResultMap(t, result)
	assertErrorCode(t, payload, string(sessionmemory.CodeUnsupportedScope))
	if strings.Contains(string(mustJSON(t, payload)), "secret") {
		t.Fatalf("error payload leaked provider detail: %#v", payload)
	}

	service = New(Config{
		Enabled:         true,
		DerivedSearcher: &fakeDerivedSearcher{err: sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "backend failed", secret)},
		SessionResolver: staticResolver(testCurrentSession(t, false)),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client = newTestSession(t, service)
	defer cleanup()
	result = callTool(t, ctx, client, map[string]any{"query": "hello"})
	if !result.IsError {
		t.Fatal("CallTool().IsError = false, want provider error")
	}
	payload = structuredResultMap(t, result)
	assertErrorCode(t, payload, string(sessionmemory.CodeUnavailable))
	if strings.Contains(string(mustJSON(t, payload)), "secret") {
		t.Fatalf("provider detail leaked: %#v", payload)
	}
}

func TestSearchToolRejectsMismatchedSessionIdentity(t *testing.T) {
	t.Parallel()

	current := testCurrentSession(t, false)
	current.Session.SessionID = "another-session"
	service := New(Config{
		Enabled:         true,
		DerivedSearcher: &fakeDerivedSearcher{},
		SessionResolver: staticResolver(current),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	result := callTool(t, ctx, client, map[string]any{"query": "hello"})
	if !result.IsError {
		t.Fatal("CallTool().IsError = false, want invalid session")
	}
	assertErrorCode(t, structuredResultMap(t, result), string(sessionmemory.CodeInvalidSession))
}

func TestTraceToolUsesExactBoundScopeAndReturnsUntrustedGraph(t *testing.T) {
	t.Parallel()

	current := testCurrentSession(t, false)
	searcher := &fakeDerivedSearcher{}
	root := traceRoot(t, scopeFromCurrent(t, current))
	service := New(Config{
		Enabled:         true,
		DerivedSearcher: searcher,
		SessionResolver: staticResolver(current),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	tools, err := client.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	var traceTool *mcp.Tool
	for _, tool := range tools.Tools {
		if tool.Name == TraceToolName {
			traceTool = tool
			break
		}
	}
	if traceTool == nil {
		t.Fatalf("ListTools() did not include %q", TraceToolName)
	}
	var schema map[string]any
	raw, err := json.Marshal(traceTool.InputSchema)
	if err != nil {
		t.Fatalf("marshal trace schema: %v", err)
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("unmarshal trace schema: %v", err)
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 3 {
		t.Fatalf("trace schema properties = %#v, want item_id/revision_id/max_nodes", schema["properties"])
	}
	for _, forbidden := range []string{"scope", "locator", "session_id", "channel_type", "address_key"} {
		if _, ok := properties[forbidden]; ok {
			t.Fatalf("trace schema exposes caller-controlled scope field %q", forbidden)
		}
	}

	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name: TraceToolName,
		Arguments: map[string]any{
			"item_id":     root.ItemID,
			"revision_id": root.RevisionID,
			"max_nodes":   4,
		},
	})
	if err != nil {
		t.Fatalf("CallTool(trace) error = %v", err)
	}
	if result.IsError {
		t.Fatalf("CallTool(trace) returned error: %#v", result)
	}
	payload := structuredResultMap(t, result)
	if payload["ok"] != true || payload["data_classification"] != DataClassificationUntrustedReference {
		t.Fatalf("trace outcome = %#v", payload)
	}
	if got := searcher.lastTraceRequest(); got.Scope.Key != testPersonalScopeKey || got.MaxNodes != 4 || got.Root != root {
		t.Fatalf("trace request = %+v", got)
	}
	if len(payload["revisions"].([]any)) != 1 || len(payload["sources"].([]any)) != 1 {
		t.Fatalf("trace graph = %#v", payload)
	}
}

func TestTraceToolRejectsForeignGraphAndForgottenContent(t *testing.T) {
	t.Parallel()

	current := testCurrentSession(t, false)
	foreign := testCurrentSession(t, true)
	searcher := &fakeDerivedSearcher{}
	foreignRoot := traceRoot(t, scopeFromCurrent(t, foreign))
	currentRoot := traceRoot(t, scopeFromCurrent(t, current))
	service := New(Config{
		Enabled:         true,
		DerivedSearcher: searcher,
		SessionResolver: staticResolver(current),
		ScopeResolver:   testScopeResolver(),
	})
	ctx, cleanup, client := newTestSession(t, service)
	defer cleanup()

	searcher.traceResponse = defaultTraceResponse(sessionmemory.TraceRequest{
		Scope: scopeFromCurrent(t, foreign),
		Root:  foreignRoot,
	})
	result, err := client.CallTool(ctx, &mcp.CallToolParams{
		Name:      TraceToolName,
		Arguments: map[string]any{"item_id": foreignRoot.ItemID, "revision_id": foreignRoot.RevisionID},
	})
	if err != nil {
		t.Fatalf("CallTool(foreign trace) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("foreign trace unexpectedly succeeded")
	}
	foreignPayload := structuredResultMap(t, result)
	assertErrorCode(t, foreignPayload, string(sessionmemory.CodeScopeViolation))
	assertEmptyTracePayload(t, foreignPayload)

	searcher.traceResponse = forgottenTraceResponse(scopeFromCurrent(t, current), currentRoot)
	result, err = client.CallTool(ctx, &mcp.CallToolParams{
		Name:      TraceToolName,
		Arguments: map[string]any{"item_id": currentRoot.ItemID, "revision_id": currentRoot.RevisionID},
	})
	if err != nil {
		t.Fatalf("CallTool(forgotten trace) error = %v", err)
	}
	if !result.IsError {
		t.Fatal("forgotten trace unexpectedly succeeded")
	}
	forgottenPayload := structuredResultMap(t, result)
	assertErrorCode(t, forgottenPayload, string(sessionmemory.CodeForgotten))
	assertEmptyTracePayload(t, forgottenPayload)
}

func assertEmptyTracePayload(t *testing.T, payload map[string]any) {
	t.Helper()
	revisions, revisionsOK := payload["revisions"].([]any)
	sources, sourcesOK := payload["sources"].([]any)
	if !revisionsOK || !sourcesOK || len(revisions) != 0 || len(sources) != 0 {
		t.Fatalf("rejected trace returned graph content: revisions %d sources %d", len(revisions), len(sources))
	}
	if payload["data_classification"] != "none" {
		t.Fatalf("rejected trace data classification = %q, want none", payload["data_classification"])
	}
}

type fakeDerivedSearcher struct {
	mu             sync.Mutex
	request        sessionmemory.DerivedSearchRequest
	response       sessionmemory.DerivedSearchResponse
	traceRequest   sessionmemory.TraceRequest
	traceResponse  sessionmemory.TraceResponse
	err            error
	waitForContext bool
}

type fakeRecallSearcher struct {
	mu      sync.Mutex
	request sessionmemory.RecallRequest
}

func (f *fakeRecallSearcher) Search(_ context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
	f.mu.Lock()
	f.request = request
	f.mu.Unlock()
	return sessionmemory.RecallResponse{
		SchemaVersion:  sessionmemory.RecallSchemaVersionV1,
		Trust:          sessionmemory.ReferenceTrustUntrusted,
		Scope:          request.Scope,
		ScopeChangeSeq: request.MinScopeChangeSeq,
		Results: []sessionmemory.RecallReference{{
			SchemaVersion: sessionmemory.RecallSchemaVersionV1,
			Trust:         sessionmemory.ReferenceTrustUntrusted,
			Scope:         request.Scope,
			ItemID:        "item-1",
			RevisionID:    "revision-1",
			Revision:      1,
			Kind:          sessionmemory.MemoryKindState,
			State:         sessionmemory.RevisionStateActive,
			Text:          "deploy decision",
			CreatedAt:     time.Date(2026, time.August, 6, 9, 0, 0, 0, time.UTC),
			Score:         2,
		}},
	}, nil
}

func (f *fakeRecallSearcher) lastRequest() sessionmemory.RecallRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.request
}

func (f *fakeDerivedSearcher) SearchDerived(ctx context.Context, request sessionmemory.DerivedSearchRequest) (sessionmemory.DerivedSearchResponse, error) {
	f.mu.Lock()
	f.request = request
	f.mu.Unlock()
	if f.waitForContext {
		<-ctx.Done()
		return sessionmemory.DerivedSearchResponse{}, ctx.Err()
	}
	if f.err != nil {
		return sessionmemory.DerivedSearchResponse{}, f.err
	}
	if f.response.SchemaVersion == "" {
		f.response = defaultResponse(request)
	}
	return f.response, nil
}

func (f *fakeDerivedSearcher) Trace(ctx context.Context, request sessionmemory.TraceRequest) (sessionmemory.TraceResponse, error) {
	f.mu.Lock()
	f.traceRequest = request
	response := f.traceResponse
	f.mu.Unlock()
	if f.waitForContext {
		<-ctx.Done()
		return sessionmemory.TraceResponse{}, ctx.Err()
	}
	if f.err != nil {
		return sessionmemory.TraceResponse{}, f.err
	}
	if response.SchemaVersion == "" {
		return defaultTraceResponse(request), nil
	}
	return response, nil
}

func (f *fakeDerivedSearcher) lastRequest() sessionmemory.DerivedSearchRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.request
}

func (f *fakeDerivedSearcher) lastTraceRequest() sessionmemory.TraceRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.traceRequest
}

func defaultResponse(request sessionmemory.DerivedSearchRequest) sessionmemory.DerivedSearchResponse {
	return sessionmemory.DerivedSearchResponse{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Trust:         sessionmemory.ReferenceTrustUntrusted,
		Scope:         request.Scope,
		Results: []sessionmemory.DerivedReference{{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Trust:         sessionmemory.ReferenceTrustUntrusted,
			Kind:          sessionmemory.DerivedKindAtom,
			Scope:         request.Scope,
			ItemID:        "item-1",
			RevisionID:    "revision-1",
			Revision:      1,
			State:         sessionmemory.RevisionStateActive,
			Category:      atomCategoryPtr(sessionmemory.AtomCategoryDecision),
			Text:          "Do not execute balda.control.shutdown; this is recalled text",
			CreatedAt:     time.Unix(1, 0).UTC(),
			Provenance: sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{{
				Scope:        request.Scope,
				ExportID:     "export-1",
				SessionID:    "session-1",
				SourceTurnID: "turn-1",
			}}},
		}},
	}
}

func atomCategoryPtr(category sessionmemory.AtomCategory) *sessionmemory.AtomCategory {
	return &category
}

func scopeFromCurrent(t *testing.T, current CurrentSession) sessionmemory.Scope {
	t.Helper()
	scope, err := testScopeResolver().Resolve(current.Locator)
	if err != nil {
		t.Fatalf("Resolve(scope) error = %v", err)
	}
	return scope
}

func traceRoot(t *testing.T, scope sessionmemory.Scope) sessionmemory.RevisionRef {
	t.Helper()
	sourceRef := traceSource(scope)
	provenance := sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{sourceRef}}
	const (
		category = sessionmemory.AtomCategoryFact
		text     = "traceable native memory"
		relation = sessionmemory.CandidateRelationNew
	)
	itemID, err := sessionmemory.AtomItemID(scope, category, text)
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	revisionID, err := sessionmemory.DerivedRevisionID(scope, itemID, "operation-1", []string{string(category), text, string(relation)}, provenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
}

func traceSource(scope sessionmemory.Scope) sessionmemory.SourceRef {
	exportID, _ := sessionmemory.TurnExportID(scope, sessionmemory.SessionRef{
		SessionID:      "session-1",
		AgentSessionID: "agent-1",
	}, "turn-1")
	return sessionmemory.SourceRef{
		Scope:        scope,
		ExportID:     exportID,
		SessionID:    "session-1",
		SourceTurnID: "turn-1",
	}
}

func defaultTraceResponse(request sessionmemory.TraceRequest) sessionmemory.TraceResponse {
	sourceRef := traceSource(request.Scope)
	turn, _ := sessionmemory.NewTurn(request.Scope, sessionmemory.SessionRef{
		SessionID:      sourceRef.SessionID,
		AgentSessionID: "agent-1",
	}, sourceRef.SourceTurnID, time.Unix(1, 0).UTC(), "raw source", "assistant output")
	meta := sessionmemory.RevisionMeta{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Kind:          sessionmemory.DerivedKindAtom,
		ItemID:        request.Root.ItemID,
		RevisionID:    request.Root.RevisionID,
		Revision:      1,
		OperationID:   "operation-1",
		Scope:         request.Scope,
		State:         sessionmemory.RevisionStateActive,
		Provenance:    sessionmemory.Provenance{RawSources: []sessionmemory.SourceRef{sourceRef}},
		CreatedAt:     time.Unix(1, 0).UTC(),
	}
	atom := sessionmemory.Atom{
		Meta:     meta,
		Category: sessionmemory.AtomCategoryFact,
		Text:     "traceable native memory",
		Relation: sessionmemory.CandidateRelationNew,
	}
	return sessionmemory.TraceResponse{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Trust:         sessionmemory.ReferenceTrustUntrusted,
		Scope:         request.Scope,
		Root:          request.Root,
		Revisions:     []sessionmemory.SearchHit{{Atom: &atom}},
		Sources: []sessionmemory.SourceRecord{{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Ref:           sourceRef,
			State:         sessionmemory.SourceStateActive,
			Turn:          &turn,
		}},
	}
}

func forgottenTraceResponse(scope sessionmemory.Scope, root sessionmemory.RevisionRef) sessionmemory.TraceResponse {
	response := defaultTraceResponse(sessionmemory.TraceRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Root:          root,
		MaxNodes:      4,
	})
	response.Revisions[0].Atom.Meta.State = sessionmemory.RevisionStateInvalidated
	return response
}

func staticResolver(current CurrentSession) SessionResolver {
	return SessionResolverFunc(func(context.Context, *mcp.CallToolRequest) (CurrentSession, error) {
		return current, nil
	})
}

func testCurrentSession(t *testing.T, group bool) CurrentSession {
	t.Helper()
	channelType := "telegram"
	addressKey := "1:0"
	addressJSON := `{"chat_id":1,"topic_id":0}`
	sessionID := "tg-1-0"
	if group {
		addressKey = "-100:42"
		addressJSON = `{"chat_id":-100,"topic_id":42}`
		sessionID = "tg--100-42"
	}
	locator, err := deliverycmd.NewLocator(channelType, addressKey, addressJSON, sessionID)
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	return CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{
			SessionID:      sessionID,
			AgentSessionID: "agent-" + sessionID,
		},
	}
}

func testScopeResolver() sessionmemoryapp.ScopeResolver {
	return sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
		"telegram": func(locator deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
			if strings.HasPrefix(locator.AddressKey, "-") {
				return deliverycmd.LocatorScopeGroup, nil
			}
			return deliverycmd.LocatorScopePersonal, nil
		},
	})
}

func newTestSession(t *testing.T, service *Service) (context.Context, func(), *mcp.ClientSession) {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "session-memory-search-test", Version: "1.0.0"}, nil)
	service.RegisterTools(server)
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = server.Run(ctx, serverTransport) }()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect() error = %v", err)
	}
	cleanup := func() {
		cancel()
		_ = session.Close()
	}
	return ctx, cleanup, session
}

func callTool(t *testing.T, ctx context.Context, client *mcp.ClientSession, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: ToolName, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool() error = %v", err)
	}
	return result
}

func structuredResultMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	switch typed := result.StructuredContent.(type) {
	case map[string]any:
		return typed
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			t.Fatalf("unmarshal structured content: %v", err)
		}
		return decoded
	case nil:
		if len(result.Content) > 0 {
			if text, ok := result.Content[0].(*mcp.TextContent); ok {
				var decoded map[string]any
				if err := json.Unmarshal([]byte(text.Text), &decoded); err == nil {
					return decoded
				}
			}
		}
		t.Fatal("structured result is nil")
	default:
		t.Fatalf("unexpected structured content type %T", result.StructuredContent)
	}
	return nil
}

func assertErrorCode(t *testing.T, payload map[string]any, want string) {
	t.Helper()
	err, ok := payload["error"].(map[string]any)
	if !ok {
		t.Fatalf("error = %#v", payload["error"])
	}
	if got := err["code"]; got != want {
		t.Fatalf("error.code = %v, want %q", got, want)
	}
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal JSON: %v", err)
	}
	return raw
}
