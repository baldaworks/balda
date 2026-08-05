package sessionmemorytest

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// StoreFactory returns a new, empty Store for one contract subtest.
type StoreFactory func() sessionmemory.Store

// RunStoreContract applies the reusable externally observable Store contract.
// Consumers should call it from a Test function with a factory for their Store.
func RunStoreContract(t *testing.T, factory StoreFactory) {
	t.Helper()
	if factory == nil {
		t.Fatal("sessionmemorytest: Store factory is required")
	}
	t.Run("workflow_replay_retrieval_and_forgetting", func(t *testing.T) {
		runWorkflowContract(t, factory())
	})
	t.Run("cas_idempotency_and_atomic_failure", func(t *testing.T) {
		runCASContract(t, factory())
	})
	t.Run("revision_history_and_reverse_provenance", func(t *testing.T) {
		runRevisionHistoryContract(t, factory())
	})
	t.Run("exact_scope_collision_isolation", func(t *testing.T) {
		runScopeIsolationContract(t, factory())
	})
	t.Run("malformed_provenance_fails_atomically", func(t *testing.T) {
		runMalformedContract(t, factory())
	})
	t.Run("same_scope_race", func(t *testing.T) {
		runSameScopeRaceContract(t, factory())
	})
	t.Run("same_operation_race", func(t *testing.T) {
		runSameOperationRaceContract(t, factory())
	})
	t.Run("concurrent_reads_revision_and_forgetting", func(t *testing.T) {
		runConcurrentLifecycleContract(t, factory())
	})
}

func runWorkflowContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:group:7:topic:3", Kind: sessionmemory.ScopeKindGroup}
	models := NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryDecision,
		Text:     "JetStream provides durable session memory",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine := contractEngine(t, store, models, models, models)
	turn := contractTurn(t, scope, "turn-1", "Use JetStream", "Decision recorded")
	atomOutcome, err := engine.ProcessTurn(context.Background(), turn)
	if err != nil {
		t.Fatalf("ProcessTurn() error = %v", err)
	}
	if len(atomOutcome.Revisions) != 1 {
		t.Fatalf("atom revisions = %d, want 1", len(atomOutcome.Revisions))
	}
	replayedAtom, err := engine.ProcessTurn(context.Background(), turn)
	if err != nil || replayedAtom.OperationID != atomOutcome.OperationID || models.Calls().Atoms != 1 {
		t.Fatalf("atom replay = %#v, error = %v, calls = %#v", replayedAtom, err, models.Calls())
	}

	boundary := contractBoundary(t, scope, "boundary-1")
	models.SetScenarios([]sessionmemory.ScenarioCandidate{{
		TopicKey: "memory",
		Title:    "Memory architecture",
		Summary:  "Durable project context",
		Atoms:    []sessionmemory.RevisionRef{atomOutcome.Revisions[0]},
	}}, nil)
	scenarioRef := contractScenarioRef(t, boundary, atomOutcome.Revisions[0])
	models.SetProfile(&sessionmemory.ProfileCandidate{
		Disposition: sessionmemory.ProfileDispositionUpsert,
		Summary:     "Durable locator profile",
		Scenarios:   []sessionmemory.RevisionRef{scenarioRef},
	}, nil)
	boundaryOutcome, err := engine.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("ProcessBoundary() error = %v", err)
	}
	if len(boundaryOutcome.Scenarios.Revisions) != 1 || len(boundaryOutcome.Profile.Revisions) != 1 {
		t.Fatalf("boundary outcome = %#v", boundaryOutcome)
	}
	if boundaryOutcome.Scenarios.Revisions[0] != scenarioRef {
		t.Fatalf("scenario ref = %#v, want %#v", boundaryOutcome.Scenarios.Revisions[0], scenarioRef)
	}
	if _, err := engine.ProcessBoundary(context.Background(), boundary); err != nil {
		t.Fatalf("replayed ProcessBoundary() error = %v", err)
	}
	if calls := models.Calls(); calls.Scenarios != 1 || calls.Profile != 1 {
		t.Fatalf("boundary replay model calls = %#v", calls)
	}

	search, err := engine.Search(context.Background(), sessionmemory.DerivedSearchRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Query:         "durable",
		Limit:         10,
	})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(search.Results) != 3 || search.Trust != sessionmemory.ReferenceTrustUntrusted {
		t.Fatalf("Search() = %#v, want three untrusted derived layers", search)
	}
	profileRef := boundaryOutcome.Profile.Revisions[0]
	trace, err := engine.Trace(context.Background(), sessionmemory.TraceRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Root:          profileRef,
		MaxNodes:      10,
	})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(trace.Revisions) != 3 || len(trace.Sources) != 1 || trace.Trust != sessionmemory.ReferenceTrustUntrusted {
		t.Fatalf("Trace() = %#v, want closed atom-scenario-profile provenance", trace)
	}

	forgottenAt := time.Date(2026, time.August, 3, 15, 0, 0, 0, time.UTC)
	forget, err := engine.ForgetSource(context.Background(), sessionmemory.ForgetSourceCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Source:        sourceRef(turn),
		ForgottenAt:   forgottenAt,
	})
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	if len(forget.Revisions) != 3 {
		t.Fatalf("forgotten revisions = %#v, want all three layers", forget.Revisions)
	}
	search, err = engine.Search(context.Background(), sessionmemory.DerivedSearchRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Query:         "durable",
		Limit:         10,
	})
	if err != nil || len(search.Results) != 0 {
		t.Fatalf("Search() after forget = %#v, error = %v", search, err)
	}
	_, err = engine.Trace(context.Background(), sessionmemory.TraceRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Root:          profileRef,
		MaxNodes:      10,
	})
	assertCode(t, err, sessionmemory.CodeForgotten)
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadScope() error = %v", err)
	}
	if len(snapshot.Sources) != 1 || snapshot.Sources[0].State != sessionmemory.SourceStateForgotten ||
		snapshot.Sources[0].Turn != nil {
		t.Fatalf("source tombstone = %#v", snapshot.Sources)
	}
	for _, meta := range allMetas(snapshot) {
		if meta.State != sessionmemory.RevisionStateInvalidated {
			t.Fatalf("revision %q state = %q, want invalidated", meta.RevisionID, meta.State)
		}
	}
}

func runCASContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:cas", Kind: sessionmemory.ScopeKindPersonal}
	models := NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryFact,
		Text:     "CAS seed",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine := contractEngine(t, store, models, models, models)
	if _, err := engine.ProcessTurn(context.Background(), contractTurn(t, scope, "seed", "seed", "seed")); err != nil {
		t.Fatalf("seed ProcessTurn() error = %v", err)
	}
	before, err := store.LoadScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadScope() error = %v", err)
	}
	request := sessionmemory.CommitRequest{
		SchemaVersion:        sessionmemory.DerivedSchemaVersionV1,
		OperationID:          "contract-cas-operation",
		Stage:                sessionmemory.OperationStageScenarios,
		Scope:                scope,
		ExpectedScopeVersion: before.Version + 1,
	}
	if _, err := store.Commit(context.Background(), request); err == nil {
		t.Fatal("Commit() with stale CAS succeeded")
	} else {
		assertCode(t, err, sessionmemory.CodeConflict)
	}
	afterFailure, err := store.LoadScope(context.Background(), scope)
	if err != nil || afterFailure.Version != before.Version || len(afterFailure.Sources) != len(before.Sources) {
		t.Fatalf("snapshot changed after failed CAS: before=%#v after=%#v error=%v", before, afterFailure, err)
	}
	request.ExpectedScopeVersion = before.Version
	first, err := store.Commit(context.Background(), request)
	if err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	replayed, err := store.Commit(context.Background(), request)
	if err != nil || replayed.OperationID != first.OperationID || replayed.ScopeVersion != first.ScopeVersion {
		t.Fatalf("Commit() replay = %#v, error = %v; want %#v", replayed, err, first)
	}
	collision := request
	collision.ExpectedScopeVersion++
	if _, err := store.Commit(context.Background(), collision); err == nil {
		t.Fatal("operation identity reuse with different content succeeded")
	} else {
		assertCode(t, err, sessionmemory.CodeConflict)
	}
}

func runRevisionHistoryContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:history", Kind: sessionmemory.ScopeKindPersonal}
	models := NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryDecision,
		Text:     "Version one",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine := contractEngine(t, store, models, models, models)
	firstTurn := contractTurn(t, scope, "history-1", "first", "first")
	first, err := engine.ProcessTurn(context.Background(), firstTurn)
	if err != nil {
		t.Fatalf("first ProcessTurn() error = %v", err)
	}
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryDecision,
		Text:     "Version two",
		Relation: sessionmemory.CandidateRelationSupersede,
		Target:   &first.Revisions[0],
	}}, nil)
	secondTurn := contractTurn(t, scope, "history-2", "second", "second")
	second, err := engine.ProcessTurn(context.Background(), secondTurn)
	if err != nil {
		t.Fatalf("second ProcessTurn() error = %v", err)
	}
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadScope() error = %v", err)
	}
	if len(snapshot.Atoms) != 2 || snapshot.Atoms[0].Meta.State != sessionmemory.RevisionStateSuperseded ||
		snapshot.Atoms[1].Meta.State != sessionmemory.RevisionStateActive || snapshot.Atoms[1].Meta.Supersedes == nil ||
		*snapshot.Atoms[1].Meta.Supersedes != first.Revisions[0] {
		t.Fatalf("revision history = %#v", snapshot.Atoms)
	}
	trace, err := engine.Trace(context.Background(), sessionmemory.TraceRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Root:          second.Revisions[0],
		MaxNodes:      10,
	})
	if err != nil {
		t.Fatalf("Trace() error = %v", err)
	}
	if len(trace.Revisions) != 2 || len(trace.Sources) != 2 {
		t.Fatalf("supersession trace = %#v", trace)
	}
	forgotten, err := engine.ForgetSource(context.Background(), sessionmemory.ForgetSourceCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Source:        sourceRef(firstTurn),
		ForgottenAt:   time.Date(2026, time.August, 3, 14, 30, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("ForgetSource() error = %v", err)
	}
	if len(forgotten.Revisions) != 2 {
		t.Fatalf("supersession cascade = %#v, want both revisions", forgotten.Revisions)
	}
}

func runScopeIsolationContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scopes := []sessionmemory.Scope{
		{Key: "contract:personal:42", Kind: sessionmemory.ScopeKindPersonal},
		{Key: "contract:group:42", Kind: sessionmemory.ScopeKindGroup},
		{Key: "contract:group:42:topic:9", Kind: sessionmemory.ScopeKindGroup},
	}
	models := NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryFact,
		Text:     "Collision-safe memory",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine := contractEngine(t, store, models, models, models)
	var wait sync.WaitGroup
	errorsByScope := make([]error, len(scopes))
	turns := make([]sessionmemory.Turn, len(scopes))
	for index := range scopes {
		turns[index] = contractTurn(t, scopes[index], "colliding-turn", "same input", "same output")
	}
	for index := range scopes {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, errorsByScope[index] = engine.ProcessTurn(
				context.Background(),
				turns[index],
			)
		}()
	}
	wait.Wait()
	for index, err := range errorsByScope {
		if err != nil {
			t.Fatalf("ProcessTurn(scope %d) error = %v", index, err)
		}
	}
	forgetAt := time.Date(2026, time.August, 3, 16, 0, 0, 0, time.UTC)
	forgetCommand := sessionmemory.ForgetScopeCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scopes[1],
		RequestID:     "forget-middle-scope",
		ForgottenAt:   forgetAt,
	}
	firstForget, err := engine.ForgetScope(context.Background(), forgetCommand)
	if err != nil {
		t.Fatalf("ForgetScope() error = %v", err)
	}
	replayedForget, err := engine.ForgetScope(context.Background(), forgetCommand)
	if err != nil || replayedForget.OperationID != firstForget.OperationID ||
		replayedForget.ScopeVersion != firstForget.ScopeVersion {
		t.Fatalf("ForgetScope() replay = %#v, error = %v; want %#v", replayedForget, err, firstForget)
	}
	for index, scope := range scopes {
		response, err := engine.Search(context.Background(), sessionmemory.DerivedSearchRequest{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Scope:         scope,
			Query:         "collision-safe",
			Limit:         10,
		})
		if err != nil {
			t.Fatalf("Search(scope %d) error = %v", index, err)
		}
		want := 1
		if index == 1 {
			want = 0
		}
		if len(response.Results) != want {
			t.Fatalf("Search(scope %d) results = %d, want %d", index, len(response.Results), want)
		}
	}
}

func runMalformedContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:malformed", Kind: sessionmemory.ScopeKindGroup}
	missingParent := sessionmemory.RevisionRef{ItemID: "missing-item", RevisionID: "missing-revision"}
	provenance := sessionmemory.Provenance{ParentRevisions: []sessionmemory.RevisionRef{missingParent}}
	itemID, err := sessionmemory.ScenarioItemID(scope, "malformed")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	revisionID, err := sessionmemory.DerivedRevisionID(
		scope,
		itemID,
		"malformed-operation",
		[]string{"malformed", "Malformed", "Missing parent"},
		provenance,
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	request := sessionmemory.CommitRequest{
		SchemaVersion:        sessionmemory.DerivedSchemaVersionV1,
		OperationID:          "malformed-operation",
		Stage:                sessionmemory.OperationStageScenarios,
		Scope:                scope,
		ExpectedScopeVersion: 0,
		Scenarios: []sessionmemory.Scenario{{
			Meta: sessionmemory.RevisionMeta{
				SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
				Kind:          sessionmemory.DerivedKindScenario,
				ItemID:        itemID,
				RevisionID:    revisionID,
				Revision:      1,
				OperationID:   "malformed-operation",
				Scope:         scope,
				State:         sessionmemory.RevisionStateActive,
				Provenance:    provenance,
				CreatedAt:     time.Date(2026, time.August, 3, 17, 0, 0, 0, time.UTC),
			},
			TopicKey: "malformed",
			Title:    "Malformed",
			Summary:  "Missing parent",
		}},
	}
	if _, err := store.Commit(context.Background(), request); err == nil {
		t.Fatal("Commit() with missing provenance parent succeeded")
	} else if !hasAnyCode(err, sessionmemory.CodeConflict, sessionmemory.CodeNotFound, sessionmemory.CodeInvalidDerived) {
		t.Fatalf("Commit() malformed provenance error = %v", err)
	}
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil || snapshot.Version != 0 || len(snapshot.Scenarios) != 0 {
		t.Fatalf("malformed commit mutated snapshot = %#v, error = %v", snapshot, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Commit(canceled, sessionmemory.CommitRequest{}); err == nil {
		t.Fatal("canceled Store commit succeeded")
	}
}

func runConcurrentLifecycleContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	sharedScope := sessionmemory.Scope{Key: "contract:concurrent:group:7:topic:3", Kind: sessionmemory.ScopeKindGroup}
	independentScope := sessionmemory.Scope{Key: "contract:concurrent:personal:42", Kind: sessionmemory.ScopeKindPersonal}
	models := NewModels()
	models.SetAtoms([]sessionmemory.AtomCandidate{{
		Category: sessionmemory.AtomCategoryFact,
		Text:     "Concurrent lifecycle memory",
		Relation: sessionmemory.CandidateRelationNew,
	}}, nil)
	engine := contractEngine(t, store, models, models, models)
	sharedTurn := contractTurn(t, sharedScope, "concurrent-shared-turn", "shared", "shared")
	sharedAtom, err := engine.ProcessTurn(context.Background(), sharedTurn)
	if err != nil {
		t.Fatalf("shared seed ProcessTurn() error = %v", err)
	}
	independentTurn := contractTurn(t, independentScope, "concurrent-independent-turn", "independent", "independent")
	independentAtom, err := engine.ProcessTurn(context.Background(), independentTurn)
	if err != nil {
		t.Fatalf("independent seed ProcessTurn() error = %v", err)
	}

	boundary := contractBoundary(t, sharedScope, "concurrent-boundary")
	models.SetScenarios([]sessionmemory.ScenarioCandidate{{
		TopicKey: "concurrent",
		Title:    "Concurrent lifecycle",
		Summary:  "Concurrent lifecycle scenario",
		Atoms:    []sessionmemory.RevisionRef{sharedAtom.Revisions[0]},
	}}, nil)
	scenarioRef := concurrentScenarioRef(t, boundary, sharedAtom.Revisions[0])
	models.SetProfile(&sessionmemory.ProfileCandidate{
		Disposition: sessionmemory.ProfileDispositionUpsert,
		Summary:     "Concurrent lifecycle profile",
		Scenarios:   []sessionmemory.RevisionRef{scenarioRef},
	}, nil)
	sharedForget := sessionmemory.ForgetSourceCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Source:        sourceRef(sharedTurn),
		ForgottenAt:   time.Date(2026, time.August, 3, 18, 0, 0, 0, time.UTC),
	}
	independentForget := sessionmemory.ForgetScopeCommand{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         independentScope,
		RequestID:     "concurrent-independent-forget",
		ForgottenAt:   time.Date(2026, time.August, 3, 18, 1, 0, 0, time.UTC),
	}

	calls := []struct {
		name string
		run  func() error
	}{
		{
			name: "shared search",
			run: func() error {
				_, err := engine.Search(context.Background(), contractSearchRequest(sharedScope))
				return err
			},
		},
		{
			name: "shared trace",
			run: func() error {
				_, err := engine.Trace(context.Background(), contractTraceRequest(sharedScope, sharedAtom.Revisions[0]))
				return err
			},
		},
		{
			name: "shared boundary revision",
			run: func() error {
				_, err := engine.ProcessBoundary(context.Background(), boundary)
				return err
			},
		},
		{
			name: "shared source forget",
			run: func() error {
				_, err := engine.ForgetSource(context.Background(), sharedForget)
				return err
			},
		},
		{
			name: "independent search",
			run: func() error {
				_, err := engine.Search(context.Background(), contractSearchRequest(independentScope))
				return err
			},
		},
		{
			name: "independent trace",
			run: func() error {
				_, err := engine.Trace(context.Background(), contractTraceRequest(independentScope, independentAtom.Revisions[0]))
				return err
			},
		},
		{
			name: "independent scope forget",
			run: func() error {
				_, err := engine.ForgetScope(context.Background(), independentForget)
				return err
			},
		},
	}
	ready := make(chan struct{}, len(calls))
	release := make(chan struct{})
	errs := make([]error, len(calls))
	var wait sync.WaitGroup
	for index := range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			ready <- struct{}{}
			<-release
			errs[index] = calls[index].run()
		}()
	}
	for range calls {
		<-ready
	}
	close(release)
	wait.Wait()

	for index, call := range calls {
		switch call.name {
		case "shared search", "independent search":
			if errs[index] != nil {
				t.Fatalf("concurrent %s error = %v", call.name, errs[index])
			}
		case "shared trace", "independent trace":
			if errs[index] != nil && !hasCode(errs[index], sessionmemory.CodeForgotten) {
				t.Fatalf("concurrent %s error = %v, want nil or forgotten", call.name, errs[index])
			}
		case "shared boundary revision":
			if errs[index] != nil && !hasAnyCode(
				errs[index],
				sessionmemory.CodeConflict,
				sessionmemory.CodeInvalidDerived,
				sessionmemory.CodeForgotten,
			) {
				t.Fatalf("concurrent boundary error = %v", errs[index])
			}
		case "shared source forget":
			if errs[index] != nil && !hasCode(errs[index], sessionmemory.CodeConflict) {
				t.Fatalf("concurrent source forget error = %v, want nil or conflict", errs[index])
			}
			if hasCode(errs[index], sessionmemory.CodeConflict) {
				if _, err := engine.ForgetSource(context.Background(), sharedForget); err != nil {
					t.Fatalf("ForgetSource() retry after concurrent CAS error = %v", err)
				}
			}
		case "independent scope forget":
			if errs[index] != nil {
				t.Fatalf("concurrent independent ForgetScope() error = %v", errs[index])
			}
		}
	}

	assertScopeForgotten(t, engine, store, sharedScope, sharedAtom.Revisions[0])
	assertScopeForgotten(t, engine, store, independentScope, independentAtom.Revisions[0])
}

func contractSearchRequest(scope sessionmemory.Scope) sessionmemory.DerivedSearchRequest {
	return sessionmemory.DerivedSearchRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Query:         "concurrent lifecycle memory",
		Limit:         10,
	}
}

func contractTraceRequest(scope sessionmemory.Scope, root sessionmemory.RevisionRef) sessionmemory.TraceRequest {
	return sessionmemory.TraceRequest{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         scope,
		Root:          root,
		MaxNodes:      10,
	}
}

func assertScopeForgotten(
	t *testing.T,
	engine *sessionmemory.Engine,
	store sessionmemory.Store,
	scope sessionmemory.Scope,
	root sessionmemory.RevisionRef,
) {
	t.Helper()
	search, err := engine.Search(context.Background(), contractSearchRequest(scope))
	if err != nil || len(search.Results) != 0 {
		t.Fatalf("Search(%q) after concurrent forget = %#v, error = %v", scope.Key, search, err)
	}
	_, err = engine.Trace(context.Background(), contractTraceRequest(scope, root))
	assertCode(t, err, sessionmemory.CodeForgotten)
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil {
		t.Fatalf("LoadScope(%q) error = %v", scope.Key, err)
	}
	for _, source := range snapshot.Sources {
		if source.State != sessionmemory.SourceStateForgotten || source.Turn != nil {
			t.Fatalf("scope %q retained readable source = %#v", scope.Key, source)
		}
	}
	for _, meta := range allMetas(snapshot) {
		if meta.State != sessionmemory.RevisionStateInvalidated {
			t.Fatalf("scope %q retained readable revision = %#v", scope.Key, meta)
		}
	}
}

func concurrentScenarioRef(
	t *testing.T,
	boundary sessionmemory.Boundary,
	atom sessionmemory.RevisionRef,
) sessionmemory.RevisionRef {
	t.Helper()
	operationID, err := sessionmemory.ProcessingOperationID(sessionmemory.OperationStageScenarios, boundary.ExportID)
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	itemID, err := sessionmemory.ScenarioItemID(boundary.Scope, "concurrent")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	revisionID, err := sessionmemory.DerivedRevisionID(
		boundary.Scope,
		itemID,
		operationID,
		[]string{"concurrent", "Concurrent lifecycle", "Concurrent lifecycle scenario"},
		sessionmemory.Provenance{ParentRevisions: []sessionmemory.RevisionRef{atom}},
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
}

func runSameScopeRaceContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:race", Kind: sessionmemory.ScopeKindGroup}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	models := NewModels()
	engines := make([]*sessionmemory.Engine, 2)
	for index := range engines {
		extractor := barrierExtractor{
			ready:   ready,
			release: release,
			candidate: sessionmemory.AtomCandidate{
				Category: sessionmemory.AtomCategoryEvent,
				Text:     fmt.Sprintf("Concurrent event %d", index),
				Relation: sessionmemory.CandidateRelationNew,
			},
		}
		engines[index] = contractEngine(t, store, extractor, models, models)
	}
	results := make([]error, len(engines))
	turns := []sessionmemory.Turn{
		contractTurn(t, scope, "race-turn-0", "race", "race"),
		contractTurn(t, scope, "race-turn-1", "race", "race"),
	}
	var wait sync.WaitGroup
	for index, engine := range engines {
		wait.Add(1)
		go func() {
			defer wait.Done()
			_, results[index] = engine.ProcessTurn(
				context.Background(),
				turns[index],
			)
		}()
	}
	<-ready
	<-ready
	close(release)
	wait.Wait()
	successes := 0
	conflicts := 0
	for _, err := range results {
		switch {
		case err == nil:
			successes++
		case hasCode(err, sessionmemory.CodeConflict):
			conflicts++
		default:
			t.Fatalf("same-scope race error = %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("same-scope race successes=%d conflicts=%d", successes, conflicts)
	}
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil || len(snapshot.Sources) != 1 || len(snapshot.Atoms) != 1 {
		t.Fatalf("same-scope race snapshot = %#v, error = %v", snapshot, err)
	}
}

func runSameOperationRaceContract(t *testing.T, store sessionmemory.Store) {
	t.Helper()
	scope := sessionmemory.Scope{Key: "contract:idempotent-race", Kind: sessionmemory.ScopeKindPersonal}
	ready := make(chan struct{}, 2)
	release := make(chan struct{})
	candidate := sessionmemory.AtomCandidate{
		Category: sessionmemory.AtomCategoryConstraint,
		Text:     "One durable result",
		Relation: sessionmemory.CandidateRelationNew,
	}
	models := NewModels()
	engines := []*sessionmemory.Engine{
		contractEngine(t, store, barrierExtractor{ready: ready, release: release, candidate: candidate}, models, models),
		contractEngine(t, store, barrierExtractor{ready: ready, release: release, candidate: candidate}, models, models),
	}
	turn := contractTurn(t, scope, "same-turn", "same", "same")
	outcomes := make([]sessionmemory.OperationOutcome, 2)
	errs := make([]error, 2)
	var wait sync.WaitGroup
	for index, engine := range engines {
		wait.Add(1)
		go func() {
			defer wait.Done()
			outcomes[index], errs[index] = engine.ProcessTurn(context.Background(), turn)
		}()
	}
	<-ready
	<-ready
	close(release)
	wait.Wait()
	if errs[0] != nil || errs[1] != nil || outcomes[0].OperationID != outcomes[1].OperationID ||
		outcomes[0].ScopeVersion != outcomes[1].ScopeVersion {
		t.Fatalf("same-operation race outcomes=%#v errors=%#v", outcomes, errs)
	}
	snapshot, err := store.LoadScope(context.Background(), scope)
	if err != nil || len(snapshot.Sources) != 1 || len(snapshot.Atoms) != 1 {
		t.Fatalf("same-operation race snapshot = %#v, error = %v", snapshot, err)
	}
}

type barrierExtractor struct {
	ready     chan<- struct{}
	release   <-chan struct{}
	candidate sessionmemory.AtomCandidate
}

func (b barrierExtractor) ExtractAtoms(
	ctx context.Context,
	_ sessionmemory.AtomExtractionRequest,
) ([]sessionmemory.AtomCandidate, error) {
	select {
	case b.ready <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case <-b.release:
		return []sessionmemory.AtomCandidate{b.candidate}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func contractEngine(
	t *testing.T,
	store sessionmemory.Store,
	atoms sessionmemory.AtomExtractor,
	scenarios sessionmemory.ScenarioSynthesizer,
	profiles sessionmemory.ProfileSynthesizer,
) *sessionmemory.Engine {
	t.Helper()
	engine, err := sessionmemory.NewEngine(store, atoms, scenarios, profiles, sessionmemory.Config{})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func contractTurn(t *testing.T, scope sessionmemory.Scope, sourceTurnID, userText, assistantText string) sessionmemory.Turn {
	t.Helper()
	turn, err := sessionmemory.NewTurn(
		scope,
		sessionmemory.SessionRef{SessionID: "colliding-session", AgentSessionID: "contract-agent-session"},
		sourceTurnID,
		time.Date(2026, time.August, 3, 12, 0, 0, 0, time.UTC),
		userText,
		assistantText,
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	return turn
}

func contractBoundary(t *testing.T, scope sessionmemory.Scope, transitionID string) sessionmemory.Boundary {
	t.Helper()
	boundary, err := sessionmemory.NewBoundary(
		scope,
		sessionmemory.SessionRef{SessionID: "colliding-session", AgentSessionID: "contract-agent-session"},
		transitionID,
		sessionmemory.BoundaryReasonReset,
		time.Date(2026, time.August, 3, 13, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	return boundary
}

func contractScenarioRef(
	t *testing.T,
	boundary sessionmemory.Boundary,
	atom sessionmemory.RevisionRef,
) sessionmemory.RevisionRef {
	t.Helper()
	operationID, err := sessionmemory.ProcessingOperationID(sessionmemory.OperationStageScenarios, boundary.ExportID)
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	itemID, err := sessionmemory.ScenarioItemID(boundary.Scope, "memory")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	revisionID, err := sessionmemory.DerivedRevisionID(
		boundary.Scope,
		itemID,
		operationID,
		[]string{"memory", "Memory architecture", "Durable project context"},
		sessionmemory.Provenance{ParentRevisions: []sessionmemory.RevisionRef{atom}},
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
}

func sourceRef(turn sessionmemory.Turn) sessionmemory.SourceRef {
	return sessionmemory.SourceRef{
		Scope:        turn.Scope,
		ExportID:     turn.ExportID,
		SessionID:    turn.Session.SessionID,
		SourceTurnID: turn.SourceTurnID,
	}
}

func allMetas(snapshot sessionmemory.ScopeSnapshot) []sessionmemory.RevisionMeta {
	metas := make([]sessionmemory.RevisionMeta, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms {
		metas = append(metas, atom.Meta)
	}
	for _, scenario := range snapshot.Scenarios {
		metas = append(metas, scenario.Meta)
	}
	for _, profile := range snapshot.Profiles {
		metas = append(metas, profile.Meta)
	}
	return metas
}

func assertCode(t *testing.T, err error, want sessionmemory.ErrorCode) {
	t.Helper()
	if !hasCode(err, want) {
		code, class, ok := sessionmemory.ClassifyError(err)
		t.Fatalf("ClassifyError(%v) = %q, %q, %v; want %q", err, code, class, ok, want)
	}
}

func hasCode(err error, want sessionmemory.ErrorCode) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	return ok && code == want
}

func hasAnyCode(err error, wants ...sessionmemory.ErrorCode) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	if !ok {
		return false
	}
	for _, want := range wants {
		if code == want {
			return true
		}
	}
	return false
}
