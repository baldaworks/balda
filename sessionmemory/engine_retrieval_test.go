package sessionmemory

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"
)

func TestSearchReturnsSortedStructuredUntrustedReferences(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	scenario := boundaryTestScenario(t, scope, atom)
	profile := boundaryTestProfile(t, scope, scenario)
	profileScore := 0.9
	scenarioScore := 0.5
	store := retrievalTestStore(t)
	store.search = func(_ context.Context, request DerivedSearchRequest) ([]SearchHit, error) {
		if request.Query != "release decision" || request.Limit != DefaultSearchLimit || request.Scope != scope {
			t.Fatalf("Store Search request = %+v", request)
		}
		return []SearchHit{
			{Atom: &atom},
			{Scenario: &scenario, Score: &scenarioScore},
			{Profile: &profile, Score: &profileScore},
		}, nil
	}
	engine := newEngineForRetrievalTest(t, store, Config{})
	response, err := engine.Search(context.Background(), DerivedSearchRequest{
		Scope: scope,
		Query: "  release decision  ",
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if response.Trust != ReferenceTrustUntrusted || len(response.Results) != 3 {
		t.Fatalf("Search() response = %+v", response)
	}
	if response.Results[0].Kind != DerivedKindProfile || response.Results[1].Kind != DerivedKindScenario ||
		response.Results[2].Kind != DerivedKindAtom {
		t.Fatalf("Search() order = %+v", response.Results)
	}
	for _, result := range response.Results {
		if err := result.Validate(); err != nil {
			t.Fatalf("DerivedReference.Validate() error = %v", err)
		}
	}
}

func TestSearchRejectsHostileStoreResults(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	inactive := atom
	inactive.Meta.State = RevisionStateInvalidated
	foreign := derivedTestAtom(t, Scope{Key: "telegram:-100:42", Kind: ScopeKindGroup})
	scenario := boundaryTestScenario(t, scope, atom)
	nan := math.NaN()
	atomKind := DerivedKindAtom
	tests := []struct {
		name     string
		request  DerivedSearchRequest
		hits     []SearchHit
		wantCode ErrorCode
	}{
		{
			name:     "foreign scope",
			request:  DerivedSearchRequest{Scope: scope, Query: "query"},
			hits:     []SearchHit{{Atom: &foreign}},
			wantCode: CodeScopeViolation,
		},
		{
			name:     "inactive revision",
			request:  DerivedSearchRequest{Scope: scope, Query: "query"},
			hits:     []SearchHit{{Atom: &inactive}},
			wantCode: CodeInvalidDerived,
		},
		{
			name:     "wrong kind",
			request:  DerivedSearchRequest{Scope: scope, Query: "query", Kind: &atomKind},
			hits:     []SearchHit{{Scenario: &scenario}},
			wantCode: CodeInvalidDerived,
		},
		{
			name:     "non-finite score",
			request:  DerivedSearchRequest{Scope: scope, Query: "query"},
			hits:     []SearchHit{{Atom: &atom, Score: &nan}},
			wantCode: CodeInvalidDerived,
		},
		{
			name:     "duplicate revision",
			request:  DerivedSearchRequest{Scope: scope, Query: "query", Limit: 2},
			hits:     []SearchHit{{Atom: &atom}, {Atom: &atom}},
			wantCode: CodeInvalidDerived,
		},
		{
			name:     "Store exceeds limit",
			request:  DerivedSearchRequest{Scope: scope, Query: "query", Limit: 1},
			hits:     []SearchHit{{Atom: &atom}, {Scenario: &scenario}},
			wantCode: CodeLimitExceeded,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := retrievalTestStore(t)
			store.search = func(context.Context, DerivedSearchRequest) ([]SearchHit, error) {
				return tt.hits, nil
			}
			engine := newEngineForRetrievalTest(t, store, Config{})
			_, err := engine.Search(context.Background(), tt.request)
			assertDerivedErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestSearchEnforcesResponseByteBound(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	store := retrievalTestStore(t)
	store.search = func(context.Context, DerivedSearchRequest) ([]SearchHit, error) {
		return []SearchHit{{Atom: &atom}}, nil
	}
	engine := newEngineForRetrievalTest(t, store, Config{MaxSearchResponseBytes: 128})
	_, err := engine.Search(context.Background(), DerivedSearchRequest{Scope: scope, Query: "query"})
	assertDerivedErrorCode(t, err, CodeLimitExceeded)
}

func TestTraceReturnsClosedProvenanceGraph(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "trace-turn", "user", "assistant")
	atom := retrievalTestAtom(t, turn)
	scenario := boundaryTestScenario(t, turn.Scope, atom)
	profile := boundaryTestProfile(t, turn.Scope, scenario)
	source := SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           sourceRefFromTurn(turn),
		State:         SourceStateActive,
		Turn:          &turn,
	}
	store := retrievalTestStore(t)
	store.trace = func(_ context.Context, request TraceRequest) (TraceGraph, error) {
		if request.MaxNodes != MaxTraceNodes {
			t.Fatalf("Trace request MaxNodes = %d, want %d", request.MaxNodes, MaxTraceNodes)
		}
		return TraceGraph{
			SchemaVersion: DerivedSchemaVersionV1,
			Scope:         turn.Scope,
			Root:          revisionRef(profile.Meta),
			Revisions: []SearchHit{
				{Profile: &profile},
				{Atom: &atom},
				{Scenario: &scenario},
			},
			Sources: []SourceRecord{source},
		}, nil
	}
	engine := newEngineForRetrievalTest(t, store, Config{})
	response, err := engine.Trace(context.Background(), TraceRequest{
		Scope: turn.Scope,
		Root:  revisionRef(profile.Meta),
	})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if response.Trust != ReferenceTrustUntrusted || len(response.Revisions) != 3 || len(response.Sources) != 1 {
		t.Fatalf("Trace() response = %+v", response)
	}
}

func TestTraceRejectsForgottenMissingAndExtraNodes(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "trace-hostile", "user", "assistant")
	atom := retrievalTestAtom(t, turn)
	source := SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           sourceRefFromTurn(turn),
		State:         SourceStateActive,
		Turn:          &turn,
	}
	forgottenAt := turn.CompletedAt.Add(time.Hour)
	forgotten := SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           source.Ref,
		State:         SourceStateForgotten,
		ForgottenAt:   &forgottenAt,
	}
	extra := derivedTestAtom(t, turn.Scope)
	invalidated := atom
	invalidated.Meta.State = RevisionStateInvalidated
	scenario := boundaryTestScenario(t, turn.Scope, atom)
	profile := boundaryTestProfile(t, turn.Scope, scenario)
	cyclicScenario := scenario
	cyclicScenario.Meta.Provenance = Provenance{ParentRevisions: []RevisionRef{revisionRef(profile.Meta)}}
	tests := []struct {
		name     string
		graph    TraceGraph
		wantCode ErrorCode
	}{
		{
			name: "forgotten source",
			graph: TraceGraph{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         turn.Scope,
				Root:          revisionRef(atom.Meta),
				Revisions:     []SearchHit{{Atom: &atom}},
				Sources:       []SourceRecord{forgotten},
			},
			wantCode: CodeForgotten,
		},
		{
			name: "invalidated revision",
			graph: TraceGraph{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         turn.Scope,
				Root:          revisionRef(invalidated.Meta),
				Revisions:     []SearchHit{{Atom: &invalidated}},
				Sources:       []SourceRecord{source},
			},
			wantCode: CodeForgotten,
		},
		{
			name: "missing source",
			graph: TraceGraph{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         turn.Scope,
				Root:          revisionRef(atom.Meta),
				Revisions:     []SearchHit{{Atom: &atom}},
			},
			wantCode: CodeNotFound,
		},
		{
			name: "extra disconnected revision",
			graph: TraceGraph{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         turn.Scope,
				Root:          revisionRef(atom.Meta),
				Revisions:     []SearchHit{{Atom: &atom}, {Atom: &extra}},
				Sources:       []SourceRecord{source},
			},
			wantCode: CodeInvalidDerived,
		},
		{
			name: "cyclic provenance",
			graph: TraceGraph{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         turn.Scope,
				Root:          revisionRef(profile.Meta),
				Revisions: []SearchHit{
					{Profile: &profile},
					{Scenario: &cyclicScenario},
				},
			},
			wantCode: CodeInvalidDerived,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := retrievalTestStore(t)
			store.trace = func(context.Context, TraceRequest) (TraceGraph, error) {
				return tt.graph, nil
			}
			engine := newEngineForRetrievalTest(t, store, Config{})
			_, err := engine.Trace(context.Background(), TraceRequest{
				Scope: turn.Scope,
				Root:  tt.graph.Root,
			})
			assertDerivedErrorCode(t, err, tt.wantCode)
		})
	}
}

func TestTraceRejectsGraphOverflowAndOversizedPayload(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "trace-limits", strings.Repeat("u", 128), strings.Repeat("a", 128))
	atom := retrievalTestAtom(t, turn)
	source := SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           sourceRefFromTurn(turn),
		State:         SourceStateActive,
		Turn:          &turn,
	}
	graph := TraceGraph{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         turn.Scope,
		Root:          revisionRef(atom.Meta),
		Revisions:     []SearchHit{{Atom: &atom}},
		Sources:       []SourceRecord{source},
	}
	store := retrievalTestStore(t)
	store.trace = func(context.Context, TraceRequest) (TraceGraph, error) { return graph, nil }

	overflowEngine := newEngineForRetrievalTest(t, store, Config{})
	_, err := overflowEngine.Trace(context.Background(), TraceRequest{
		Scope:    turn.Scope,
		Root:     graph.Root,
		MaxNodes: 1,
	})
	assertDerivedErrorCode(t, err, CodeLimitExceeded)

	sizeEngine := newEngineForRetrievalTest(t, store, Config{MaxSearchResponseBytes: 128})
	_, err = sizeEngine.Trace(context.Background(), TraceRequest{Scope: turn.Scope, Root: graph.Root})
	assertDerivedErrorCode(t, err, CodeLimitExceeded)
}

func retrievalTestStore(t *testing.T) *engineTestStore {
	t.Helper()
	return &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			t.Fatal("LookupOperation() called by retrieval")
			return OperationLookupResult{}, nil
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			t.Fatal("LoadScope() called by retrieval")
			return ScopeSnapshot{}, nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			t.Fatal("Commit() called by retrieval")
			return OperationOutcome{}, nil
		},
	}
}

func newEngineForRetrievalTest(t *testing.T, store Store, config Config) *Engine {
	t.Helper()
	return newEngineForAtomTest(t, store, engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return nil, nil
	}), config)
}

func retrievalTestAtom(t *testing.T, turn Turn) Atom {
	t.Helper()
	source := sourceRefFromTurn(turn)
	itemID, err := AtomItemID(turn.Scope, AtomCategoryFact, "Traceable fact")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	provenance := Provenance{RawSources: []SourceRef{source}}
	revisionID, err := DerivedRevisionID(
		turn.Scope,
		itemID,
		"trace-atom-operation",
		[]string{string(AtomCategoryFact), "Traceable fact", string(CandidateRelationNew)},
		provenance,
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return Atom{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindAtom,
			ItemID:        itemID,
			RevisionID:    revisionID,
			Revision:      1,
			OperationID:   "trace-atom-operation",
			Scope:         turn.Scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     turn.CompletedAt,
		},
		Category: AtomCategoryFact,
		Text:     "Traceable fact",
		Relation: CandidateRelationNew,
	}
}
