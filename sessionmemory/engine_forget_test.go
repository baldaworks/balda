package sessionmemory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestForgetSourceCascadesThroughSupersededHistoryAndReplays(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:group:77:topic:9", Kind: ScopeKindGroup}
	firstTurn := engineTestTurn(t, scope, "turn-1", "We chose JetStream", "Recorded")
	secondTurn := engineTestTurn(t, scope, "turn-2", "Keep the other fact", "Recorded")
	firstSource := activeSourceRecord(firstTurn)
	secondSource := activeSourceRecord(secondTurn)
	dependentAtom := retrievalTestAtom(t, firstTurn)
	dependentAtom.Meta.State = RevisionStateSuperseded
	unrelatedAtom := retrievalTestAtom(t, secondTurn)
	scenario := boundaryTestScenario(t, scope, dependentAtom)
	profile := boundaryTestProfile(t, scope, scenario)
	store := newForgetStateStore(ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       7,
		Sources:       []SourceRecord{firstSource, secondSource},
		Atoms:         []Atom{dependentAtom, unrelatedAtom},
		Scenarios:     []Scenario{scenario},
		Profiles:      []Profile{profile},
	})
	engine := newEngineForRetrievalTest(t, store, Config{})
	command := ForgetSourceCommand{
		SchemaVersion: DerivedSchemaVersionV1,
		Source:        firstSource.Ref,
		ForgottenAt:   time.Date(2026, time.August, 3, 10, 0, 0, 0, time.UTC),
	}

	outcome, err := engine.ForgetSource(context.Background(), command)
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	wantRevisions := []RevisionRef{
		revisionRef(dependentAtom.Meta),
		revisionRef(scenario.Meta),
		revisionRef(profile.Meta),
	}
	if outcome.ScopeVersion != 8 || !sameRevisionRefSet(outcome.Revisions, wantRevisions) {
		t.Fatalf("ForgetSource() outcome = %#v, want version 8 and cascade %#v", outcome, wantRevisions)
	}
	assertForgottenSource(t, store.snapshot.Sources[0], command.ForgottenAt)
	if store.snapshot.Atoms[0].Meta.State != RevisionStateInvalidated ||
		store.snapshot.Scenarios[0].Meta.State != RevisionStateInvalidated ||
		store.snapshot.Profiles[0].Meta.State != RevisionStateInvalidated {
		t.Fatalf("dependent states = %q, %q, %q; want invalidated",
			store.snapshot.Atoms[0].Meta.State,
			store.snapshot.Scenarios[0].Meta.State,
			store.snapshot.Profiles[0].Meta.State,
		)
	}
	if store.snapshot.Sources[1].State != SourceStateActive || store.snapshot.Atoms[1].Meta.State != RevisionStateActive {
		t.Fatal("forgetting one source changed unrelated same-scope memory")
	}

	replayed, err := engine.ForgetSource(context.Background(), command)
	if err != nil {
		t.Fatalf("replayed ForgetSource() error = %v", err)
	}
	if !sameRevisionRefSet(replayed.Revisions, outcome.Revisions) || store.loadCalls != 1 || store.forgetCalls != 1 {
		t.Fatalf("replay = %#v, load calls = %d, forget calls = %d", replayed, store.loadCalls, store.forgetCalls)
	}
}

func TestForgetSourceTraversesInvalidatedParentAndRejectsIdentityCollision(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:personal:42", Kind: ScopeKindPersonal}
	turn := engineTestTurn(t, scope, "turn-1", "private", "recorded")
	source := activeSourceRecord(turn)
	atom := retrievalTestAtom(t, turn)
	atom.Meta.State = RevisionStateInvalidated
	scenario := boundaryTestScenario(t, scope, atom)
	store := newForgetStateStore(ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       3,
		Sources:       []SourceRecord{source},
		Atoms:         []Atom{atom},
		Scenarios:     []Scenario{scenario},
	})
	engine := newEngineForRetrievalTest(t, store, Config{})
	colliding := source.Ref
	colliding.SessionID = "another-session"

	_, err := engine.ForgetSource(context.Background(), ForgetSourceCommand{
		SchemaVersion: DerivedSchemaVersionV1,
		Source:        colliding,
		ForgottenAt:   derivedTestTime(),
	})
	assertDerivedErrorCode(t, err, CodeNotFound)
	if store.forgetCalls != 0 {
		t.Fatal("colliding source identity reached Store mutation")
	}

	outcome, err := engine.ForgetSource(context.Background(), ForgetSourceCommand{
		SchemaVersion: DerivedSchemaVersionV1,
		Source:        source.Ref,
		ForgottenAt:   derivedTestTime(),
	})
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	if len(outcome.Revisions) != 1 || outcome.Revisions[0] != revisionRef(scenario.Meta) {
		t.Fatalf("invalidated-parent cascade = %#v, want active child only", outcome.Revisions)
	}
}

func TestForgetScopeOnlyMutatesReadableExactScopeContent(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:group:shared", Kind: ScopeKindGroup}
	activeTurn := engineTestTurn(t, scope, "active", "active text", "answer")
	forgottenTurn := engineTestTurn(t, scope, "forgotten", "secret text", "answer")
	activeSource := activeSourceRecord(activeTurn)
	forgottenAt := derivedTestTime()
	forgottenSource := SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           sourceRefFromTurn(forgottenTurn),
		State:         SourceStateForgotten,
		ForgottenAt:   &forgottenAt,
	}
	activeAtom := retrievalTestAtom(t, activeTurn)
	invalidatedAtom := retrievalTestAtom(t, forgottenTurn)
	invalidatedAtom.Meta.State = RevisionStateInvalidated
	store := newForgetStateStore(ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       11,
		Sources:       []SourceRecord{activeSource, forgottenSource},
		Atoms:         []Atom{activeAtom, invalidatedAtom},
	})
	engine := newEngineForRetrievalTest(t, store, Config{})
	command := ForgetScopeCommand{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		RequestID:     "erase-request-1",
		ForgottenAt:   time.Date(2026, time.August, 3, 11, 0, 0, 0, time.UTC),
	}

	outcome, err := engine.ForgetScope(context.Background(), command)
	if err != nil {
		t.Fatalf("ForgetScope() error = %v", err)
	}
	if outcome.Scope != scope || outcome.ScopeVersion != 12 ||
		len(outcome.Sources) != 1 || outcome.Sources[0] != activeSource.Ref ||
		len(outcome.Revisions) != 1 || outcome.Revisions[0] != revisionRef(activeAtom.Meta) {
		t.Fatalf("ForgetScope() outcome = %#v", outcome)
	}
	assertForgottenSource(t, store.snapshot.Sources[0], command.ForgottenAt)
	if store.snapshot.Sources[1].ForgottenAt == nil || *store.snapshot.Sources[1].ForgottenAt != forgottenAt {
		t.Fatal("scope forget rewrote an existing source tombstone")
	}
	if store.snapshot.Atoms[1].Meta.State != RevisionStateInvalidated {
		t.Fatal("scope forget changed an already invalidated revision")
	}
}

func TestForgetSourceRejectsCyclicProvenanceBeforeMutation(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:group:cycle", Kind: ScopeKindGroup}
	turn := engineTestTurn(t, scope, "turn-1", "source", "answer")
	source := activeSourceRecord(turn)
	atom := retrievalTestAtom(t, turn)
	scenario := boundaryTestScenario(t, scope, atom)
	profile := boundaryTestProfile(t, scope, scenario)
	scenario.Meta.Provenance = Provenance{ParentRevisions: []RevisionRef{revisionRef(profile.Meta)}}
	store := newForgetStateStore(ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       1,
		Sources:       []SourceRecord{source},
		Atoms:         []Atom{atom},
		Scenarios:     []Scenario{scenario},
		Profiles:      []Profile{profile},
	})
	engine := newEngineForRetrievalTest(t, store, Config{})

	_, err := engine.ForgetSource(context.Background(), ForgetSourceCommand{
		SchemaVersion: DerivedSchemaVersionV1,
		Source:        source.Ref,
		ForgottenAt:   derivedTestTime(),
	})
	assertDerivedErrorCode(t, err, CodeInvalidDerived)
	if store.forgetCalls != 0 || store.snapshot.Sources[0].Turn == nil {
		t.Fatal("cyclic provenance caused a partial forgetting mutation")
	}
}

func TestForgetFailsClosedOnStoreOutcomeMismatchAndCancellation(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:personal:99", Kind: ScopeKindPersonal}
	turn := engineTestTurn(t, scope, "turn-1", "secret", "answer")
	source := activeSourceRecord(turn)
	store := &engineTestStore{
		lookup: func(context.Context, OperationLookup) (OperationLookupResult, error) {
			return OperationLookupResult{}, nil
		},
		lookupForget: func(context.Context, ForgetLookup) (ForgetLookupResult, error) {
			return ForgetLookupResult{}, nil
		},
		load: func(context.Context, Scope) (ScopeSnapshot, error) {
			return ScopeSnapshot{
				SchemaVersion: DerivedSchemaVersionV1,
				Scope:         scope,
				Version:       1,
				Sources:       []SourceRecord{source},
			}, nil
		},
		commit: func(context.Context, CommitRequest) (OperationOutcome, error) { return OperationOutcome{}, nil },
		forgetSource: func(_ context.Context, request ForgetSourceRequest) (ForgetOutcome, error) {
			return ForgetOutcome{
				SchemaVersion: DerivedSchemaVersionV1,
				OperationID:   request.OperationID,
				Kind:          ForgetKindSource,
				Scope:         request.Scope,
				ScopeVersion:  request.ExpectedScopeVersion + 1,
			}, nil
		},
	}
	engine := newEngineForRetrievalTest(t, store, Config{})
	command := ForgetSourceCommand{SchemaVersion: DerivedSchemaVersionV1, Source: source.Ref, ForgottenAt: derivedTestTime()}
	_, err := engine.ForgetSource(context.Background(), command)
	assertDerivedErrorCode(t, err, CodeInvalidDerived)

	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = engine.ForgetSource(canceled, command)
	code, class, ok := ClassifyError(err)
	if !ok || code != CodeTimeout || class != ErrorClassRetryable {
		t.Fatalf("ClassifyError(%v) = %q, %q, %v; want timeout, retryable, true", err, code, class, ok)
	}
}

type forgetStateStore struct {
	snapshot    ScopeSnapshot
	operations  map[string]ForgetOutcome
	loadCalls   int
	forgetCalls int
}

func newForgetStateStore(snapshot ScopeSnapshot) *forgetStateStore {
	return &forgetStateStore{snapshot: cloneScopeSnapshot(snapshot), operations: make(map[string]ForgetOutcome)}
}

func (s *forgetStateStore) LookupOperation(context.Context, OperationLookup) (OperationLookupResult, error) {
	return OperationLookupResult{}, nil
}

func (s *forgetStateStore) LookupForget(_ context.Context, lookup ForgetLookup) (ForgetLookupResult, error) {
	outcome, ok := s.operations[lookup.OperationID]
	return ForgetLookupResult{Found: ok, Outcome: cloneForgetOutcome(outcome)}, nil
}

func (s *forgetStateStore) LoadScope(_ context.Context, scope Scope) (ScopeSnapshot, error) {
	s.loadCalls++
	if scope != s.snapshot.Scope {
		return ScopeSnapshot{}, PermanentError(CodeScopeViolation, "foreign scope", nil)
	}
	return cloneScopeSnapshot(s.snapshot), nil
}

func (s *forgetStateStore) Commit(context.Context, CommitRequest) (OperationOutcome, error) {
	return OperationOutcome{}, errors.New("unexpected commit")
}

func (s *forgetStateStore) ForgetSource(_ context.Context, request ForgetSourceRequest) (ForgetOutcome, error) {
	s.forgetCalls++
	if request.ExpectedScopeVersion != s.snapshot.Version {
		return ForgetOutcome{}, PermanentError(CodeConflict, "version conflict", nil)
	}
	for index := range s.snapshot.Sources {
		if s.snapshot.Sources[index].Ref == request.Source && s.snapshot.Sources[index].State == SourceStateActive {
			tombstoneSource(&s.snapshot.Sources[index], request.ForgottenAt)
		}
	}
	applyForgetTransitions(&s.snapshot, request.ExpectedRevisions)
	s.snapshot.Version++
	outcome := ForgetOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          ForgetKindSource,
		Scope:         request.Scope,
		ScopeVersion:  s.snapshot.Version,
		Sources:       []SourceRef{request.Source},
		Revisions:     append([]RevisionRef(nil), request.ExpectedRevisions...),
	}
	s.operations[request.OperationID] = cloneForgetOutcome(outcome)
	return outcome, nil
}

func (s *forgetStateStore) ForgetScope(_ context.Context, request ForgetScopeRequest) (ForgetOutcome, error) {
	s.forgetCalls++
	if request.ExpectedScopeVersion != s.snapshot.Version {
		return ForgetOutcome{}, PermanentError(CodeConflict, "version conflict", nil)
	}
	for index := range s.snapshot.Sources {
		for _, ref := range request.ExpectedSources {
			if s.snapshot.Sources[index].Ref == ref && s.snapshot.Sources[index].State == SourceStateActive {
				tombstoneSource(&s.snapshot.Sources[index], request.ForgottenAt)
			}
		}
	}
	applyForgetTransitions(&s.snapshot, request.ExpectedRevisions)
	s.snapshot.Version++
	outcome := ForgetOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          ForgetKindScope,
		Scope:         request.Scope,
		ScopeVersion:  s.snapshot.Version,
		Sources:       append([]SourceRef(nil), request.ExpectedSources...),
		Revisions:     append([]RevisionRef(nil), request.ExpectedRevisions...),
	}
	s.operations[request.OperationID] = cloneForgetOutcome(outcome)
	return outcome, nil
}

func (s *forgetStateStore) Search(context.Context, DerivedSearchRequest) ([]SearchHit, error) {
	return nil, nil
}

func (s *forgetStateStore) Trace(context.Context, TraceRequest) (TraceGraph, error) {
	return TraceGraph{}, nil
}

func activeSourceRecord(turn Turn) SourceRecord {
	turnCopy := cloneTurn(turn)
	return SourceRecord{
		SchemaVersion: DerivedSchemaVersionV1,
		Ref:           sourceRefFromTurn(turn),
		State:         SourceStateActive,
		Turn:          &turnCopy,
	}
}

func tombstoneSource(record *SourceRecord, forgottenAt time.Time) {
	record.State = SourceStateForgotten
	record.Turn = nil
	record.ForgottenAt = &forgottenAt
}

func applyForgetTransitions(snapshot *ScopeSnapshot, refs []RevisionRef) {
	set := make(map[RevisionRef]struct{}, len(refs))
	for _, ref := range refs {
		set[ref] = struct{}{}
	}
	for index := range snapshot.Atoms {
		if _, ok := set[revisionRef(snapshot.Atoms[index].Meta)]; ok {
			snapshot.Atoms[index].Meta.State = RevisionStateInvalidated
		}
	}
	for index := range snapshot.Scenarios {
		if _, ok := set[revisionRef(snapshot.Scenarios[index].Meta)]; ok {
			snapshot.Scenarios[index].Meta.State = RevisionStateInvalidated
		}
	}
	for index := range snapshot.Profiles {
		if _, ok := set[revisionRef(snapshot.Profiles[index].Meta)]; ok {
			snapshot.Profiles[index].Meta.State = RevisionStateInvalidated
		}
	}
}

func assertForgottenSource(t *testing.T, source SourceRecord, forgottenAt time.Time) {
	t.Helper()
	if source.State != SourceStateForgotten || source.Turn != nil || source.ForgottenAt == nil || *source.ForgottenAt != forgottenAt {
		t.Fatalf("source after forget = %#v", source)
	}
}
