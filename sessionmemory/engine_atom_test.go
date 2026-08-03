package sessionmemory

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestProcessTurnCommitsEngineOwnedAtomOnce(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-1", "Remember this", "We will ship the package")
	events := make([]string, 0, 4)
	store := &engineTestStore{
		lookup: func(_ context.Context, lookup OperationLookup) (OperationLookupResult, error) {
			events = append(events, "lookup")
			if err := lookup.Validate(); err != nil {
				t.Fatalf("lookup.Validate() error = %v", err)
			}
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			events = append(events, "load")
			return emptyEngineSnapshot(scope), nil
		},
		commit: func(_ context.Context, request CommitRequest) (OperationOutcome, error) {
			events = append(events, "commit")
			if err := request.Validate(); err != nil {
				t.Fatalf("CommitRequest.Validate() error = %v", err)
			}
			if len(request.Sources) != 1 || len(request.Atoms) != 1 {
				t.Fatalf("commit writes = %d sources, %d atoms", len(request.Sources), len(request.Atoms))
			}
			atom := request.Atoms[0]
			if atom.Text != "Ship the package" || atom.Meta.Scope != turn.Scope || atom.Meta.CreatedAt != turn.CompletedAt {
				t.Fatalf("engine-owned atom = %+v", atom)
			}
			return outcomeForCommit(request), nil
		},
	}
	extractor := engineAtomExtractorFunc(func(_ context.Context, request AtomExtractionRequest) ([]AtomCandidate, error) {
		events = append(events, "model")
		if request.Turn.Scope != turn.Scope || request.View.Scope != turn.Scope {
			t.Fatalf("model request scopes = %+v, %+v", request.Turn.Scope, request.View.Scope)
		}
		return []AtomCandidate{{
			Category: AtomCategoryDecision,
			Text:     "  Ship   the package ",
			Relation: CandidateRelationNew,
		}}, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})

	outcome, err := engine.ProcessTurn(context.Background(), turn)
	if err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
	if len(outcome.Revisions) != 1 || outcome.ScopeVersion != 1 {
		t.Fatalf("ProcessTurn() outcome = %+v", outcome)
	}
	if got, want := strings.Join(events, ","), "lookup,load,model,commit"; got != want {
		t.Fatalf("call order = %q, want %q", got, want)
	}
}

func TestNewEngineRequiresPortsAndBoundedConfig(t *testing.T) {
	t.Parallel()

	store := &engineTestStore{}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return nil, nil
	})
	scenarios := engineScenarioSynthesizer{}
	profile := engineProfileSynthesizer{}
	tests := []struct {
		name      string
		store     Store
		extractor AtomExtractor
		scenarios ScenarioSynthesizer
		profile   ProfileSynthesizer
		config    Config
	}{
		{name: "missing Store", extractor: extractor, scenarios: scenarios, profile: profile},
		{name: "missing atom extractor", store: store, scenarios: scenarios, profile: profile},
		{name: "missing scenario synthesizer", store: store, extractor: extractor, profile: profile},
		{name: "missing profile synthesizer", store: store, extractor: extractor, scenarios: scenarios},
		{
			name:      "raised hard ceiling",
			store:     store,
			extractor: extractor,
			scenarios: scenarios,
			profile:   profile,
			config:    Config{MaxCandidateCount: MaxCandidateCount + 1},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine, err := NewEngine(tt.store, tt.extractor, tt.scenarios, tt.profile, tt.config)
			if err == nil || engine != nil {
				t.Fatalf("NewEngine() = %+v, %v; want nil, error", engine, err)
			}
		})
	}
}

func TestProcessTurnReplaySkipsSnapshotModelAndCommit(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-replay", "user", "assistant")
	operationID, err := ProcessingOperationID(OperationStageAtoms, turn.ExportID)
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	want := OperationOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Stage:         OperationStageAtoms,
		Scope:         turn.Scope,
		ScopeVersion:  7,
	}
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{Found: true, Outcome: want}, nil
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			t.Fatal("LoadScope() called during replay")
			return ScopeSnapshot{}, nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			t.Fatal("Commit() called during replay")
			return OperationOutcome{}, nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		t.Fatal("ExtractAtoms() called during replay")
		return nil, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})

	got, err := engine.ProcessTurn(context.Background(), turn)
	if err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
	if got.OperationID != want.OperationID || got.ScopeVersion != want.ScopeVersion {
		t.Fatalf("ProcessTurn() = %+v, want %+v", got, want)
	}
}

func TestProcessTurnModelViewContainsOnlyActiveRevisions(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	invalidated := derivedTestAtom(t, scope)
	invalidated.Meta.State = RevisionStateInvalidated
	turn := engineTestTurn(t, scope, "turn-active-view", "user", "assistant")
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			snapshot := emptyEngineSnapshot(scope)
			snapshot.Atoms = []Atom{invalidated}
			return snapshot, nil
		},
		commit: func(_ context.Context, request CommitRequest) (OperationOutcome, error) {
			return outcomeForCommit(request), nil
		},
	}
	extractor := engineAtomExtractorFunc(func(_ context.Context, request AtomExtractionRequest) ([]AtomCandidate, error) {
		if len(request.View.Atoms) != 0 {
			t.Fatalf("model view atoms = %+v, want active-only empty view", request.View.Atoms)
		}
		return nil, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	if _, err := engine.ProcessTurn(context.Background(), turn); err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
}

func TestProcessTurnGroundsCrossItemSupersession(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	prior := derivedTestAtom(t, scope)
	priorRef := RevisionRef{ItemID: prior.Meta.ItemID, RevisionID: prior.Meta.RevisionID}
	turn := engineTestTurn(t, scope, "turn-2", "Change the decision", "Do not ship it")
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			snapshot := emptyEngineSnapshot(scope)
			snapshot.Version = 4
			snapshot.Atoms = []Atom{prior}
			return snapshot, nil
		},
		commit: func(_ context.Context, request CommitRequest) (OperationOutcome, error) {
			if len(request.Atoms) != 1 || len(request.Transitions) != 1 {
				t.Fatalf("commit = %d atoms, %d transitions", len(request.Atoms), len(request.Transitions))
			}
			atom := request.Atoms[0]
			if atom.Meta.ItemID == prior.Meta.ItemID || atom.Meta.Revision != 1 {
				t.Fatalf("cross-item atom identity = %+v", atom.Meta)
			}
			if atom.RelatedRevision == nil || *atom.RelatedRevision != priorRef || atom.Meta.Supersedes == nil {
				t.Fatalf("supersession links = %+v", atom)
			}
			transition := request.Transitions[0]
			if transition.Ref != priorRef || transition.From != RevisionStateActive || transition.To != RevisionStateSuperseded {
				t.Fatalf("transition = %+v", transition)
			}
			return outcomeForCommit(request), nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return []AtomCandidate{{
			Category: AtomCategoryDecision,
			Text:     "Do not ship the public memory package",
			Relation: CandidateRelationSupersede,
			Target:   &priorRef,
		}}, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	if _, err := engine.ProcessTurn(context.Background(), turn); err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
}

func TestProcessTurnRejectsHostileInputsBeforeCommit(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	turn := engineTestTurn(t, scope, "turn-hostile", "user", "assistant")
	foreign := Scope{Key: "telegram:-100:42", Kind: ScopeKindGroup}
	tests := []struct {
		name      string
		config    Config
		load      func(Scope) ScopeSnapshot
		candidate AtomCandidate
		wantCode  ErrorCode
	}{
		{
			name: "foreign snapshot",
			load: func(Scope) ScopeSnapshot {
				return emptyEngineSnapshot(foreign)
			},
			candidate: AtomCandidate{Category: AtomCategoryFact, Text: "fact", Relation: CandidateRelationNew},
			wantCode:  CodeScopeViolation,
		},
		{
			name: "ungrounded target",
			load: emptyEngineSnapshot,
			candidate: AtomCandidate{
				Category: AtomCategoryFact,
				Text:     "fact",
				Relation: CandidateRelationSupersede,
				Target:   &RevisionRef{ItemID: "missing-item", RevisionID: "missing-revision"},
			},
			wantCode: CodeInvalidDerived,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			commitCalls := 0
			store := &engineTestStore{
				lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
					return OperationLookupResult{}, nil
				},
				load: func(_ context.Context, gotScope Scope) (ScopeSnapshot, error) {
					return tt.load(gotScope), nil
				},
				commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
					commitCalls++
					return OperationOutcome{}, nil
				},
			}
			extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
				return []AtomCandidate{tt.candidate}, nil
			})
			engine := newEngineForAtomTest(t, store, extractor, tt.config)
			_, err := engine.ProcessTurn(context.Background(), turn)
			assertDerivedErrorCode(t, err, tt.wantCode)
			if commitCalls != 0 {
				t.Fatalf("Commit() calls = %d, want 0", commitCalls)
			}
		})
	}
}

func TestProcessTurnReturnsConflictWithoutImplicitRetry(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-conflict", "user", "assistant")
	modelCalls := 0
	commitCalls := 0
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			return emptyEngineSnapshot(scope), nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			commitCalls++
			return OperationOutcome{}, PermanentError(CodeConflict, "scope version changed", nil)
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		modelCalls++
		return []AtomCandidate{{Category: AtomCategoryFact, Text: "fact", Relation: CandidateRelationNew}}, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	_, err := engine.ProcessTurn(context.Background(), turn)
	assertDerivedErrorCode(t, err, CodeConflict)
	if modelCalls != 1 || commitCalls != 1 {
		t.Fatalf("calls = %d model, %d commit; want 1 each", modelCalls, commitCalls)
	}
}

func TestProcessTurnEnforcesConfiguredBoundsAndCancellation(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-bounds", "12345", "67890")
	storeCalls := 0
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			storeCalls++
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			return emptyEngineSnapshot(scope), nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			return OperationOutcome{}, nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return nil, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{MaxTurnTextBytes: 9})
	_, err := engine.ProcessTurn(context.Background(), turn)
	assertDerivedErrorCode(t, err, CodeLimitExceeded)
	if storeCalls != 0 {
		t.Fatalf("Store calls = %d, want 0 before oversized turn rejection", storeCalls)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.ProcessTurn(ctx, engineTestTurn(t, derivedTestScope(), "turn-canceled", "1", "2"))
	code, class, ok := ClassifyError(err)
	if !ok || code != CodeTimeout || class != ErrorClassRetryable {
		t.Fatalf("cancellation error = %v; class = %q, %q, %v", err, code, class, ok)
	}
}

func TestProcessTurnEnforcesCandidateBoundsBeforeCommit(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-candidate-bounds", "user", "assistant")
	commitCalls := 0
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			return emptyEngineSnapshot(scope), nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			commitCalls++
			return OperationOutcome{}, nil
		},
	}
	tests := []struct {
		name       string
		config     Config
		candidates []AtomCandidate
	}{
		{
			name:   "candidate count",
			config: Config{MaxCandidateCount: 1},
			candidates: []AtomCandidate{
				{Category: AtomCategoryFact, Text: "first", Relation: CandidateRelationNew},
				{Category: AtomCategoryFact, Text: "second", Relation: CandidateRelationNew},
			},
		},
		{
			name:       "raw text before normalization",
			config:     Config{MaxDerivedTextBytes: 4},
			candidates: []AtomCandidate{{Category: AtomCategoryFact, Text: "     ", Relation: CandidateRelationNew}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
				return tt.candidates, nil
			})
			engine := newEngineForAtomTest(t, store, extractor, tt.config)
			_, err := engine.ProcessTurn(context.Background(), turn)
			assertDerivedErrorCode(t, err, CodeLimitExceeded)
		})
	}
	if commitCalls != 0 {
		t.Fatalf("Commit() calls = %d, want 0", commitCalls)
	}
}

func TestProcessTurnRejectsForgottenSourceBeforeModel(t *testing.T) {
	t.Parallel()

	turn := engineTestTurn(t, derivedTestScope(), "turn-forgotten", "user", "assistant")
	forgottenAt := turn.CompletedAt.Add(time.Hour)
	modelCalls := 0
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			snapshot := emptyEngineSnapshot(scope)
			snapshot.Sources = []SourceRecord{{
				SchemaVersion: DerivedSchemaVersionV1,
				Ref:           sourceRefFromTurn(turn),
				State:         SourceStateForgotten,
				ForgottenAt:   &forgottenAt,
			}}
			return snapshot, nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			t.Fatal("Commit() called for forgotten source")
			return OperationOutcome{}, nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		modelCalls++
		return nil, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	_, err := engine.ProcessTurn(context.Background(), turn)
	assertDerivedErrorCode(t, err, CodeForgotten)
	if modelCalls != 0 {
		t.Fatalf("model calls = %d, want 0", modelCalls)
	}
}

func TestProcessTurnRedactsUnknownPortErrors(t *testing.T) {
	t.Parallel()

	secret := "secret backend detail"
	turn := engineTestTurn(t, derivedTestScope(), "turn-model-error", "user", "assistant")
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		load: func(_ context.Context, scope Scope) (ScopeSnapshot, error) {
			return emptyEngineSnapshot(scope), nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			return OperationOutcome{}, nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return nil, errors.New(secret)
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	_, err := engine.ProcessTurn(context.Background(), turn)
	code, class, ok := ClassifyError(err)
	if !ok || code != CodeModelFailure || class != ErrorClassRetryable {
		t.Fatalf("model error = %v; class = %q, %q, %v", err, code, class, ok)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("model error exposed backend detail: %v", err)
	}
}

func TestProcessTurnRedactsClassifiedStoreError(t *testing.T) {
	t.Parallel()

	secret := "secret Store detail"
	turn := engineTestTurn(t, derivedTestScope(), "turn-store-error", "user", "assistant")
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, PermanentError(CodeConflict, secret, errors.New(secret))
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			t.Fatal("LoadScope() called after lookup error")
			return ScopeSnapshot{}, nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) {
			t.Fatal("Commit() called after lookup error")
			return OperationOutcome{}, nil
		},
	}
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		t.Fatal("ExtractAtoms() called after lookup error")
		return nil, nil
	})
	engine := newEngineForAtomTest(t, store, extractor, Config{})
	_, err := engine.ProcessTurn(context.Background(), turn)
	assertDerivedErrorCode(t, err, CodeConflict)
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("Store error exposed backend detail: %v", err)
	}
}

type engineTestStore struct {
	lookup       func(context.Context, OperationLookup) (OperationLookupResult, error)
	lookupForget func(context.Context, ForgetLookup) (ForgetLookupResult, error)
	load         func(context.Context, Scope) (ScopeSnapshot, error)
	commit       func(context.Context, CommitRequest) (OperationOutcome, error)
	forgetSource func(context.Context, ForgetSourceRequest) (ForgetOutcome, error)
	forgetScope  func(context.Context, ForgetScopeRequest) (ForgetOutcome, error)
	search       func(context.Context, DerivedSearchRequest) ([]SearchHit, error)
	trace        func(context.Context, TraceRequest) (TraceGraph, error)
}

func (s *engineTestStore) LookupOperation(ctx context.Context, lookup OperationLookup) (OperationLookupResult, error) {
	return s.lookup(ctx, lookup)
}

func (s *engineTestStore) LookupForget(ctx context.Context, lookup ForgetLookup) (ForgetLookupResult, error) {
	if s.lookupForget == nil {
		return ForgetLookupResult{}, nil
	}
	return s.lookupForget(ctx, lookup)
}

func (s *engineTestStore) LoadScope(ctx context.Context, scope Scope) (ScopeSnapshot, error) {
	return s.load(ctx, scope)
}

func (s *engineTestStore) Commit(ctx context.Context, request CommitRequest) (OperationOutcome, error) {
	return s.commit(ctx, request)
}

func (s *engineTestStore) ForgetSource(ctx context.Context, request ForgetSourceRequest) (ForgetOutcome, error) {
	if s.forgetSource == nil {
		return ForgetOutcome{}, nil
	}
	return s.forgetSource(ctx, request)
}

func (s *engineTestStore) ForgetScope(ctx context.Context, request ForgetScopeRequest) (ForgetOutcome, error) {
	if s.forgetScope == nil {
		return ForgetOutcome{}, nil
	}
	return s.forgetScope(ctx, request)
}

func (s *engineTestStore) Search(ctx context.Context, request DerivedSearchRequest) ([]SearchHit, error) {
	if s.search == nil {
		return nil, nil
	}
	return s.search(ctx, request)
}

func (s *engineTestStore) Trace(ctx context.Context, request TraceRequest) (TraceGraph, error) {
	if s.trace == nil {
		return TraceGraph{}, nil
	}
	return s.trace(ctx, request)
}

type engineAtomExtractorFunc func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error)

func (f engineAtomExtractorFunc) ExtractAtoms(ctx context.Context, request AtomExtractionRequest) ([]AtomCandidate, error) {
	return f(ctx, request)
}

type engineScenarioSynthesizer struct{}

func (engineScenarioSynthesizer) SynthesizeScenarios(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
	return nil, nil
}

type engineProfileSynthesizer struct{}

func (engineProfileSynthesizer) SynthesizeProfile(context.Context, ProfileSynthesisRequest) (*ProfileCandidate, error) {
	return nil, nil
}

func newEngineForAtomTest(t *testing.T, store Store, extractor AtomExtractor, config Config) *Engine {
	t.Helper()
	engine, err := NewEngine(store, extractor, engineScenarioSynthesizer{}, engineProfileSynthesizer{}, config)
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func engineTestTurn(t *testing.T, scope Scope, sourceTurnID, userText, assistantText string) Turn {
	t.Helper()
	turn, err := NewTurn(
		scope,
		SessionRef{SessionID: "session-1", AgentSessionID: "agent-session-1"},
		sourceTurnID,
		time.Date(2026, time.August, 3, 8, 0, 0, 0, time.UTC),
		userText,
		assistantText,
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	return turn
}

func emptyEngineSnapshot(scope Scope) ScopeSnapshot {
	return ScopeSnapshot{SchemaVersion: DerivedSchemaVersionV1, Scope: scope}
}

func outcomeForCommit(request CommitRequest) OperationOutcome {
	refs := make([]RevisionRef, 0, len(request.Atoms)+len(request.Scenarios)+len(request.Profiles))
	for _, atom := range request.Atoms {
		refs = append(refs, RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID})
	}
	for _, scenario := range request.Scenarios {
		refs = append(refs, RevisionRef{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID})
	}
	for _, profile := range request.Profiles {
		refs = append(refs, RevisionRef{ItemID: profile.Meta.ItemID, RevisionID: profile.Meta.RevisionID})
	}
	return OperationOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Stage:         request.Stage,
		Scope:         request.Scope,
		ScopeVersion:  request.ExpectedScopeVersion + 1,
		Revisions:     refs,
	}
}
