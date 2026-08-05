package sessionmemory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProcessBoundaryCommitsStagesAndReplaysWithoutModels(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	atomRef := revisionRef(atom.Meta)
	store := newBoundaryTestStore(scope, atom)
	scenarioCalls := 0
	profileCalls := 0
	scenarios := engineScenarioSynthesizerFunc(func(_ context.Context, request ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
		scenarioCalls++
		if len(request.View.Atoms) != 1 || request.View.Atoms[0].Meta.State != RevisionStateActive {
			t.Fatalf("scenario model view = %+v", request.View)
		}
		return []ScenarioCandidate{{
			TopicKey: "release",
			Title:    " Release ",
			Summary:  " Ship   the package ",
			Atoms:    []RevisionRef{atomRef},
		}}, nil
	})
	profiles := engineProfileSynthesizerFunc(func(_ context.Context, request ProfileSynthesisRequest) (*ProfileCandidate, error) {
		profileCalls++
		if len(request.View.Scenarios) != 1 {
			t.Fatalf("profile model scenarios = %+v", request.View.Scenarios)
		}
		return &ProfileCandidate{
			Disposition: ProfileDispositionUpsert,
			Summary:     "Prefers portable packages",
			Scenarios:   []RevisionRef{revisionRef(request.View.Scenarios[0].Meta)},
		}, nil
	})
	engine := newEngineForBoundaryTest(t, store, scenarios, profiles)
	boundary := engineTestBoundary(t, scope, "boundary-1")

	first, err := engine.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("ProcessBoundary() error = %v", err)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("BoundaryOutcome.Validate() error = %v", err)
	}
	if first.Scenarios.ScopeVersion != 2 || first.Profile.ScopeVersion != 3 {
		t.Fatalf("stage versions = %d, %d; want 2, 3", first.Scenarios.ScopeVersion, first.Profile.ScopeVersion)
	}
	if scenarioCalls != 1 || profileCalls != 1 || store.commitCalls != 2 {
		t.Fatalf("first calls = %d scenario, %d profile, %d commit", scenarioCalls, profileCalls, store.commitCalls)
	}

	second, err := engine.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("ProcessBoundary() replay error = %v", err)
	}
	if second.Scenarios.OperationID != first.Scenarios.OperationID || second.Profile.OperationID != first.Profile.OperationID {
		t.Fatalf("replay outcome = %+v, want stable %+v", second, first)
	}
	if scenarioCalls != 1 || profileCalls != 1 || store.commitCalls != 2 {
		t.Fatalf("replay calls = %d scenario, %d profile, %d commit; want unchanged", scenarioCalls, profileCalls, store.commitCalls)
	}
}

func TestProcessBoundaryResumesAfterProfileFailure(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	store := newBoundaryTestStore(scope, atom)
	scenarioCalls := 0
	profileCalls := 0
	scenarios := engineScenarioSynthesizerFunc(func(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
		scenarioCalls++
		return []ScenarioCandidate{{
			TopicKey: "release",
			Title:    "Release",
			Summary:  "Release context",
			Atoms:    []RevisionRef{revisionRef(atom.Meta)},
		}}, nil
	})
	profiles := engineProfileSynthesizerFunc(func(_ context.Context, request ProfileSynthesisRequest) (*ProfileCandidate, error) {
		profileCalls++
		if profileCalls == 1 {
			return nil, errors.New("temporary model failure")
		}
		return &ProfileCandidate{
			Disposition: ProfileDispositionUpsert,
			Summary:     "Release profile",
			Scenarios:   []RevisionRef{revisionRef(request.View.Scenarios[0].Meta)},
		}, nil
	})
	engine := newEngineForBoundaryTest(t, store, scenarios, profiles)
	boundary := engineTestBoundary(t, scope, "boundary-partial")

	partial, err := engine.ProcessBoundary(context.Background(), boundary)
	code, class, ok := ClassifyError(err)
	if !ok || code != CodeModelFailure || class != ErrorClassRetryable {
		t.Fatalf("first ProcessBoundary() error = %v; class = %q, %q, %v", err, code, class, ok)
	}
	if partial.Scenarios.OperationID == "" || partial.Profile.OperationID != "" {
		t.Fatalf("partial outcome = %+v", partial)
	}
	if store.commitCalls != 1 {
		t.Fatalf("commits after partial failure = %d, want 1", store.commitCalls)
	}

	outcome, err := engine.ProcessBoundary(context.Background(), boundary)
	if err != nil {
		t.Fatalf("second ProcessBoundary() error = %v", err)
	}
	if err := outcome.Validate(); err != nil {
		t.Fatalf("resumed outcome validation error = %v", err)
	}
	if scenarioCalls != 1 || profileCalls != 2 || store.commitCalls != 2 {
		t.Fatalf("resume calls = %d scenario, %d profile, %d commit", scenarioCalls, profileCalls, store.commitCalls)
	}
}

func TestProcessBoundarySupersedesCurrentScenarioAndProfile(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	priorScenario := boundaryTestScenario(t, scope, atom)
	priorProfile := boundaryTestProfile(t, scope, priorScenario)
	store := newBoundaryTestStore(scope, atom)
	store.snapshot.Scenarios = []Scenario{priorScenario}
	store.snapshot.Profiles = []Profile{priorProfile}
	scenarios := engineScenarioSynthesizerFunc(func(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
		prior := revisionRef(priorScenario.Meta)
		return []ScenarioCandidate{{
			TopicKey:   priorScenario.TopicKey,
			Title:      "Release update",
			Summary:    "Updated release context",
			Atoms:      []RevisionRef{revisionRef(atom.Meta)},
			Supersedes: &prior,
		}}, nil
	})
	profiles := engineProfileSynthesizerFunc(func(_ context.Context, request ProfileSynthesisRequest) (*ProfileCandidate, error) {
		prior := revisionRef(priorProfile.Meta)
		var activeScenario RevisionRef
		for _, scenario := range request.View.Scenarios {
			if scenario.Meta.State == RevisionStateActive {
				activeScenario = revisionRef(scenario.Meta)
			}
		}
		return &ProfileCandidate{
			Disposition: ProfileDispositionUpsert,
			Summary:     "Updated release profile",
			Scenarios:   []RevisionRef{activeScenario},
			Supersedes:  &prior,
		}, nil
	})
	engine := newEngineForBoundaryTest(t, store, scenarios, profiles)
	if _, err := engine.ProcessBoundary(context.Background(), engineTestBoundary(t, scope, "boundary-update")); err != nil {
		t.Fatalf("ProcessBoundary() error = %v", err)
	}

	if len(store.snapshot.Scenarios) != 2 || store.snapshot.Scenarios[0].Meta.State != RevisionStateSuperseded ||
		store.snapshot.Scenarios[1].Meta.State != RevisionStateActive || store.snapshot.Scenarios[1].Meta.Revision != 2 {
		t.Fatalf("scenario history = %+v", store.snapshot.Scenarios)
	}
	if len(store.snapshot.Profiles) != 2 || store.snapshot.Profiles[0].Meta.State != RevisionStateSuperseded ||
		store.snapshot.Profiles[1].Meta.State != RevisionStateActive || store.snapshot.Profiles[1].Meta.Revision != 2 {
		t.Fatalf("profile history = %+v", store.snapshot.Profiles)
	}
}

func TestProcessBoundaryRejectsInactiveOrMissingParentsBeforeCommit(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	atom.Meta.State = RevisionStateInvalidated
	store := newBoundaryTestStore(scope, atom)
	missing := revisionRef(atom.Meta)
	scenarios := engineScenarioSynthesizerFunc(func(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
		return []ScenarioCandidate{{
			TopicKey: "invalid",
			Title:    "Invalid",
			Summary:  "Must not commit",
			Atoms:    []RevisionRef{missing},
		}}, nil
	})
	profiles := engineProfileSynthesizerFunc(func(context.Context, ProfileSynthesisRequest) (*ProfileCandidate, error) {
		t.Fatal("profile model called after invalid scenario")
		return nil, nil
	})
	engine := newEngineForBoundaryTest(t, store, scenarios, profiles)
	_, err := engine.ProcessBoundary(context.Background(), engineTestBoundary(t, scope, "boundary-invalid"))
	assertDerivedErrorCode(t, err, CodeInvalidDerived)
	if store.commitCalls != 0 {
		t.Fatalf("Commit() calls = %d, want 0", store.commitCalls)
	}
}

func TestProcessBoundaryProfileConflictDoesNotRetry(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	store := newBoundaryTestStore(scope, atom)
	store.failStage = OperationStageProfile
	scenarioCalls := 0
	profileCalls := 0
	scenarios := engineScenarioSynthesizerFunc(func(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error) {
		scenarioCalls++
		return []ScenarioCandidate{{
			TopicKey: "release",
			Title:    "Release",
			Summary:  "Release context",
			Atoms:    []RevisionRef{revisionRef(atom.Meta)},
		}}, nil
	})
	profiles := engineProfileSynthesizerFunc(func(_ context.Context, request ProfileSynthesisRequest) (*ProfileCandidate, error) {
		profileCalls++
		return &ProfileCandidate{
			Disposition: ProfileDispositionUpsert,
			Summary:     "Release profile",
			Scenarios:   []RevisionRef{revisionRef(request.View.Scenarios[0].Meta)},
		}, nil
	})
	engine := newEngineForBoundaryTest(t, store, scenarios, profiles)
	partial, err := engine.ProcessBoundary(context.Background(), engineTestBoundary(t, scope, "boundary-conflict"))
	assertDerivedErrorCode(t, err, CodeConflict)
	if partial.Scenarios.OperationID == "" || partial.Profile.OperationID != "" {
		t.Fatalf("partial conflict outcome = %+v", partial)
	}
	if scenarioCalls != 1 || profileCalls != 1 || store.commitCalls != 2 {
		t.Fatalf("conflict calls = %d scenario, %d profile, %d commit; want 1, 1, 2", scenarioCalls, profileCalls, store.commitCalls)
	}
}

func TestGroundProfileCandidateSkipProducesNoRevision(t *testing.T) {
	t.Parallel()

	profiles, transitions, err := (&Engine{}).groundProfileCandidate(
		Boundary{},
		"operation-1",
		ScopeSnapshot{},
		&ProfileCandidate{Disposition: ProfileDispositionSkip},
	)
	if err != nil {
		t.Fatalf("groundProfileCandidate() error = %v", err)
	}
	if len(profiles) != 0 || len(transitions) != 0 {
		t.Fatalf("skip profile mutation = profiles=%+v transitions=%+v", profiles, transitions)
	}
}

type engineScenarioSynthesizerFunc func(context.Context, ScenarioSynthesisRequest) ([]ScenarioCandidate, error)

func (f engineScenarioSynthesizerFunc) SynthesizeScenarios(
	ctx context.Context,
	request ScenarioSynthesisRequest,
) ([]ScenarioCandidate, error) {
	return f(ctx, request)
}

type engineProfileSynthesizerFunc func(context.Context, ProfileSynthesisRequest) (*ProfileCandidate, error)

func (f engineProfileSynthesizerFunc) SynthesizeProfile(
	ctx context.Context,
	request ProfileSynthesisRequest,
) (*ProfileCandidate, error) {
	return f(ctx, request)
}

type boundaryTestStore struct {
	snapshot    ScopeSnapshot
	operations  map[string]OperationOutcome
	commitCalls int
	failStage   OperationStage
}

func newBoundaryTestStore(scope Scope, atoms ...Atom) *boundaryTestStore {
	return &boundaryTestStore{
		snapshot: ScopeSnapshot{
			SchemaVersion: DerivedSchemaVersionV1,
			Scope:         scope,
			Version:       1,
			Atoms:         cloneAtoms(atoms),
		},
		operations: make(map[string]OperationOutcome),
	}
}

func (s *boundaryTestStore) LookupOperation(_ context.Context, lookup OperationLookup) (OperationLookupResult, error) {
	outcome, ok := s.operations[lookup.OperationID]
	return OperationLookupResult{Found: ok, Outcome: cloneOperationOutcome(outcome)}, nil
}

func (s *boundaryTestStore) LookupForget(context.Context, ForgetLookup) (ForgetLookupResult, error) {
	return ForgetLookupResult{}, nil
}

func (s *boundaryTestStore) LoadScope(_ context.Context, _ Scope) (ScopeSnapshot, error) {
	return cloneScopeSnapshot(s.snapshot), nil
}

func (s *boundaryTestStore) Commit(_ context.Context, request CommitRequest) (OperationOutcome, error) {
	s.commitCalls++
	if request.Stage == s.failStage {
		return OperationOutcome{}, PermanentError(CodeConflict, "scope version changed", nil)
	}
	if request.ExpectedScopeVersion != s.snapshot.Version {
		return OperationOutcome{}, PermanentError(CodeConflict, "scope version changed", nil)
	}
	if prior, ok := s.operations[request.OperationID]; ok {
		return cloneOperationOutcome(prior), nil
	}
	for _, transition := range request.Transitions {
		if !applyBoundaryTestTransition(&s.snapshot, transition) {
			return OperationOutcome{}, invalidDerived("test Store transition target is missing")
		}
	}
	s.snapshot.Sources = append(s.snapshot.Sources, cloneSources(request.Sources)...)
	s.snapshot.Atoms = append(s.snapshot.Atoms, cloneAtoms(request.Atoms)...)
	s.snapshot.Scenarios = append(s.snapshot.Scenarios, cloneScenarios(request.Scenarios)...)
	s.snapshot.Profiles = append(s.snapshot.Profiles, cloneProfiles(request.Profiles)...)
	s.snapshot.Version++
	outcome := outcomeForCommit(request)
	s.operations[request.OperationID] = cloneOperationOutcome(outcome)
	return outcome, nil
}

func (s *boundaryTestStore) ForgetSource(context.Context, ForgetSourceRequest) (ForgetOutcome, error) {
	return ForgetOutcome{}, nil
}

func (s *boundaryTestStore) ForgetScope(context.Context, ForgetScopeRequest) (ForgetOutcome, error) {
	return ForgetOutcome{}, nil
}

func (s *boundaryTestStore) Search(context.Context, DerivedSearchRequest) ([]SearchHit, error) {
	return nil, nil
}

func (s *boundaryTestStore) Trace(context.Context, TraceRequest) (TraceGraph, error) {
	return TraceGraph{}, nil
}

func applyBoundaryTestTransition(snapshot *ScopeSnapshot, transition RevisionTransition) bool {
	for index := range snapshot.Atoms {
		if revisionRef(snapshot.Atoms[index].Meta) == transition.Ref && snapshot.Atoms[index].Meta.State == transition.From {
			snapshot.Atoms[index].Meta.State = transition.To
			return true
		}
	}
	for index := range snapshot.Scenarios {
		if revisionRef(snapshot.Scenarios[index].Meta) == transition.Ref && snapshot.Scenarios[index].Meta.State == transition.From {
			snapshot.Scenarios[index].Meta.State = transition.To
			return true
		}
	}
	for index := range snapshot.Profiles {
		if revisionRef(snapshot.Profiles[index].Meta) == transition.Ref && snapshot.Profiles[index].Meta.State == transition.From {
			snapshot.Profiles[index].Meta.State = transition.To
			return true
		}
	}
	return false
}

func newEngineForBoundaryTest(
	t *testing.T,
	store Store,
	scenarios ScenarioSynthesizer,
	profiles ProfileSynthesizer,
) *Engine {
	t.Helper()
	extractor := engineAtomExtractorFunc(func(context.Context, AtomExtractionRequest) ([]AtomCandidate, error) {
		return nil, nil
	})
	engine, err := NewEngine(store, extractor, scenarios, profiles, Config{})
	if err != nil {
		t.Fatalf("NewEngine() error = %v", err)
	}
	return engine
}

func engineTestBoundary(t *testing.T, scope Scope, transitionID string) Boundary {
	t.Helper()
	boundary, err := NewBoundary(
		scope,
		SessionRef{SessionID: "session-1", AgentSessionID: "agent-session-1"},
		transitionID,
		BoundaryReasonReset,
		time.Date(2026, time.August, 3, 9, 0, 0, 0, time.UTC),
	)
	if err != nil {
		t.Fatalf("NewBoundary() error = %v", err)
	}
	return boundary
}

func boundaryTestScenario(t *testing.T, scope Scope, atom Atom) Scenario {
	t.Helper()
	itemID, err := ScenarioItemID(scope, "release")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	provenance := Provenance{ParentRevisions: []RevisionRef{revisionRef(atom.Meta)}}
	revisionID, err := DerivedRevisionID(
		scope,
		itemID,
		"prior-scenario-operation",
		[]string{"release", "Release", "Prior release context"},
		provenance,
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return Scenario{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindScenario,
			ItemID:        itemID,
			RevisionID:    revisionID,
			Revision:      1,
			OperationID:   "prior-scenario-operation",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     derivedTestTime(),
		},
		TopicKey: "release",
		Title:    "Release",
		Summary:  "Prior release context",
	}
}

func boundaryTestProfile(t *testing.T, scope Scope, scenario Scenario) Profile {
	t.Helper()
	itemID, err := ProfileItemID(scope)
	if err != nil {
		t.Fatalf("ProfileItemID() error = %v", err)
	}
	provenance := Provenance{ParentRevisions: []RevisionRef{revisionRef(scenario.Meta)}}
	revisionID, err := DerivedRevisionID(
		scope,
		itemID,
		"prior-profile-operation",
		[]string{"Prior profile"},
		provenance,
		nil,
	)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	return Profile{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindProfile,
			ItemID:        itemID,
			RevisionID:    revisionID,
			Revision:      1,
			OperationID:   "prior-profile-operation",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     derivedTestTime(),
		},
		Summary: "Prior profile",
	}
}
