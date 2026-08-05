package sessionmemory

import "context"

// ProcessBoundary synthesizes scenarios and then the exact-scope profile as
// two independently idempotent stages.
func (e *Engine) ProcessBoundary(ctx context.Context, boundary Boundary) (BoundaryOutcome, error) {
	partial := BoundaryOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         boundary.Scope,
	}
	if e == nil {
		return partial, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return partial, err
	}
	if err := boundary.Validate(); err != nil {
		return partial, err
	}

	scenarios, err := e.processScenarioStage(ctx, boundary)
	partial.Scenarios = scenarios
	if err != nil {
		return partial, err
	}
	profile, err := e.processProfileStage(ctx, boundary)
	partial.Profile = profile
	if err != nil {
		return partial, err
	}
	if err := partial.Validate(); err != nil {
		return partial, err
	}
	return partial, nil
}

func (e *Engine) processScenarioStage(ctx context.Context, boundary Boundary) (OperationOutcome, error) {
	operationID, err := ProcessingOperationID(OperationStageScenarios, boundary.ExportID, e.config.Derivation)
	if err != nil {
		return OperationOutcome{}, err
	}
	lookup := OperationLookup{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Stage:         OperationStageScenarios,
		Scope:         boundary.Scope,
	}
	prior, err := e.store.LookupOperation(ctx, lookup)
	if err != nil {
		return OperationOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if err := prior.Validate(lookup); err != nil {
		return OperationOutcome{}, err
	}
	if prior.Found {
		return cloneOperationOutcome(prior.Outcome), nil
	}

	snapshot, err := e.loadBoundarySnapshot(ctx, boundary.Scope)
	if err != nil {
		return OperationOutcome{}, err
	}
	candidates, err := e.scenarioSynthesizer.SynthesizeScenarios(ctx, ScenarioSynthesisRequest{
		SchemaVersion: DerivedSchemaVersionV1,
		Derivation:    e.config.Derivation,
		Boundary:      boundary,
		View:          scopeViewFromSnapshot(snapshot),
	})
	if err != nil {
		return OperationOutcome{}, modelPortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if len(candidates) > e.config.MaxCandidateCount {
		return OperationOutcome{}, limitExceeded("scenario synthesis exceeds the candidate limit")
	}
	scenarios, transitions, err := e.groundScenarioCandidates(boundary, operationID, snapshot, candidates)
	if err != nil {
		return OperationOutcome{}, err
	}
	commit := CommitRequest{
		SchemaVersion:        DerivedSchemaVersionV1,
		OperationID:          operationID,
		Stage:                OperationStageScenarios,
		Scope:                boundary.Scope,
		ExpectedScopeVersion: snapshot.Version,
		Scenarios:            scenarios,
		Transitions:          transitions,
	}
	return e.commitBoundaryStage(ctx, commit)
}

func (e *Engine) processProfileStage(ctx context.Context, boundary Boundary) (OperationOutcome, error) {
	operationID, err := ProcessingOperationID(OperationStageProfile, boundary.ExportID, e.config.Derivation)
	if err != nil {
		return OperationOutcome{}, err
	}
	lookup := OperationLookup{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Stage:         OperationStageProfile,
		Scope:         boundary.Scope,
	}
	prior, err := e.store.LookupOperation(ctx, lookup)
	if err != nil {
		return OperationOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if err := prior.Validate(lookup); err != nil {
		return OperationOutcome{}, err
	}
	if prior.Found {
		return cloneOperationOutcome(prior.Outcome), nil
	}

	snapshot, err := e.loadBoundarySnapshot(ctx, boundary.Scope)
	if err != nil {
		return OperationOutcome{}, err
	}
	candidate, err := e.profileSynthesizer.SynthesizeProfile(ctx, ProfileSynthesisRequest{
		SchemaVersion: DerivedSchemaVersionV1,
		Derivation:    e.config.Derivation,
		Boundary:      boundary,
		View:          scopeViewFromSnapshot(snapshot),
	})
	if err != nil {
		return OperationOutcome{}, modelPortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	profiles, transitions, err := e.groundProfileCandidate(boundary, operationID, snapshot, candidate)
	if err != nil {
		return OperationOutcome{}, err
	}
	commit := CommitRequest{
		SchemaVersion:        DerivedSchemaVersionV1,
		OperationID:          operationID,
		Stage:                OperationStageProfile,
		Scope:                boundary.Scope,
		ExpectedScopeVersion: snapshot.Version,
		Profiles:             profiles,
		Transitions:          transitions,
	}
	return e.commitBoundaryStage(ctx, commit)
}

func (e *Engine) loadBoundarySnapshot(ctx context.Context, scope Scope) (ScopeSnapshot, error) {
	snapshot, err := e.store.LoadScope(ctx, scope)
	if err != nil {
		return ScopeSnapshot{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return ScopeSnapshot{}, err
	}
	snapshot = cloneScopeSnapshot(snapshot)
	if err := snapshot.Validate(e.config.MaxSnapshotItems); err != nil {
		return ScopeSnapshot{}, err
	}
	if snapshot.Scope != scope {
		return ScopeSnapshot{}, PermanentError(CodeScopeViolation, "Store snapshot scope does not match the boundary", nil)
	}
	return snapshot, nil
}

func (e *Engine) commitBoundaryStage(ctx context.Context, request CommitRequest) (OperationOutcome, error) {
	if err := request.Validate(); err != nil {
		return OperationOutcome{}, err
	}
	outcome, err := e.store.Commit(ctx, cloneCommitRequest(request))
	if err != nil {
		return OperationOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if err := validateCommittedOutcome(request, outcome); err != nil {
		return OperationOutcome{}, err
	}
	return cloneOperationOutcome(outcome), nil
}

func (e *Engine) groundScenarioCandidates(
	boundary Boundary,
	operationID string,
	snapshot ScopeSnapshot,
	candidates []ScenarioCandidate,
) ([]Scenario, []RevisionTransition, error) {
	activeAtoms := make(map[RevisionRef]struct{})
	for _, atom := range snapshot.Atoms {
		if atom.Meta.State == RevisionStateActive {
			activeAtoms[revisionRef(atom.Meta)] = struct{}{}
		}
	}
	activeScenarios := make(map[RevisionRef]Scenario)
	activeItems := make(map[string]RevisionRef)
	maxRevision := make(map[string]uint64)
	for _, scenario := range snapshot.Scenarios {
		ref := revisionRef(scenario.Meta)
		if scenario.Meta.Revision > maxRevision[scenario.Meta.ItemID] {
			maxRevision[scenario.Meta.ItemID] = scenario.Meta.Revision
		}
		if scenario.Meta.State == RevisionStateActive {
			activeScenarios[ref] = scenario
			if _, exists := activeItems[scenario.Meta.ItemID]; exists {
				return nil, nil, invalidDerived("Store snapshot contains multiple active revisions for one scenario")
			}
			activeItems[scenario.Meta.ItemID] = ref
		}
	}

	createdItems := make(map[string]struct{}, len(candidates))
	transitioned := make(map[RevisionRef]struct{}, len(candidates))
	scenarios := make([]Scenario, 0, len(candidates))
	transitions := make([]RevisionTransition, 0, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.TopicKey) > e.config.MaxDerivedTextBytes ||
			len(candidate.Title) > e.config.MaxDerivedTextBytes ||
			len(candidate.Summary) > e.config.MaxDerivedTextBytes {
			return nil, nil, limitExceeded("scenario candidate text exceeds the configured limit")
		}
		if err := candidate.Validate(); err != nil {
			return nil, nil, err
		}
		if len(candidate.Atoms) > e.config.MaxSourcesPerRevision {
			return nil, nil, limitExceeded("scenario candidate exceeds the configured provenance limit")
		}
		for _, atom := range candidate.Atoms {
			if _, ok := activeAtoms[atom]; !ok {
				return nil, nil, invalidDerived("scenario candidate references a non-active same-scope atom")
			}
		}
		topicKey := normalizeDerivedText(candidate.TopicKey)
		title := normalizeDerivedText(candidate.Title)
		summary := normalizeDerivedText(candidate.Summary)
		itemID, err := ScenarioItemID(boundary.Scope, topicKey)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := createdItems[itemID]; exists {
			return nil, nil, invalidDerived("scenario synthesis contains duplicate topic items")
		}
		createdItems[itemID] = struct{}{}

		var supersedes *RevisionRef
		parents := append([]RevisionRef(nil), candidate.Atoms...)
		if candidate.Supersedes != nil {
			target := *candidate.Supersedes
			if _, ok := activeScenarios[target]; !ok || target.ItemID != itemID {
				return nil, nil, invalidDerived("scenario supersession target is not the active same-topic revision")
			}
			if _, exists := transitioned[target]; exists {
				return nil, nil, invalidDerived("scenario synthesis targets one revision more than once")
			}
			transitioned[target] = struct{}{}
			targetCopy := target
			supersedes = &targetCopy
			parents = appendUniqueRevisionRef(parents, target)
			transitions = append(transitions, RevisionTransition{
				Ref:  target,
				From: RevisionStateActive,
				To:   RevisionStateSuperseded,
			})
		}
		if prior, exists := activeItems[itemID]; exists {
			if supersedes == nil || prior != *supersedes {
				return nil, nil, invalidDerived("scenario candidate would create a second active topic revision")
			}
		} else if supersedes != nil {
			return nil, nil, invalidDerived("scenario candidate supersedes a non-current topic revision")
		}
		if len(parents) > e.config.MaxSourcesPerRevision {
			return nil, nil, limitExceeded("scenario provenance exceeds the configured limit")
		}
		provenance := Provenance{ParentRevisions: parents}
		revisionID, err := DerivedRevisionID(
			boundary.Scope,
			itemID,
			operationID,
			[]string{topicKey, title, summary},
			provenance,
			supersedes,
		)
		if err != nil {
			return nil, nil, err
		}
		scenario := Scenario{
			Meta: RevisionMeta{
				SchemaVersion: DerivedSchemaVersionV1,
				Kind:          DerivedKindScenario,
				ItemID:        itemID,
				RevisionID:    revisionID,
				Revision:      maxRevision[itemID] + 1,
				OperationID:   operationID,
				Scope:         boundary.Scope,
				State:         RevisionStateActive,
				Provenance:    provenance,
				CreatedAt:     boundary.OccurredAt,
				Supersedes:    supersedes,
			},
			TopicKey: topicKey,
			Title:    title,
			Summary:  summary,
		}
		if err := scenario.Validate(); err != nil {
			return nil, nil, err
		}
		scenarios = append(scenarios, scenario)
	}
	return scenarios, transitions, nil
}

func (e *Engine) groundProfileCandidate(
	boundary Boundary,
	operationID string,
	snapshot ScopeSnapshot,
	candidate *ProfileCandidate,
) ([]Profile, []RevisionTransition, error) {
	if candidate == nil {
		return nil, nil, nil
	}
	if candidate.Disposition == ProfileDispositionSkip {
		if err := candidate.Validate(); err != nil {
			return nil, nil, err
		}
		return nil, nil, nil
	}
	if len(candidate.Summary) > e.config.MaxDerivedTextBytes {
		return nil, nil, limitExceeded("profile candidate text exceeds the configured limit")
	}
	if err := candidate.Validate(); err != nil {
		return nil, nil, err
	}
	if len(candidate.Atoms)+len(candidate.Scenarios) > e.config.MaxSourcesPerRevision {
		return nil, nil, limitExceeded("profile candidate exceeds the configured provenance limit")
	}
	activeAtoms := make(map[RevisionRef]struct{})
	for _, atom := range snapshot.Atoms {
		if atom.Meta.State == RevisionStateActive {
			activeAtoms[revisionRef(atom.Meta)] = struct{}{}
		}
	}
	activeScenarios := make(map[RevisionRef]struct{})
	for _, scenario := range snapshot.Scenarios {
		if scenario.Meta.State == RevisionStateActive {
			activeScenarios[revisionRef(scenario.Meta)] = struct{}{}
		}
	}
	for _, atom := range candidate.Atoms {
		if _, ok := activeAtoms[atom]; !ok {
			return nil, nil, invalidDerived("profile candidate references a non-active same-scope atom")
		}
	}
	for _, scenario := range candidate.Scenarios {
		if _, ok := activeScenarios[scenario]; !ok {
			return nil, nil, invalidDerived("profile candidate references a non-active same-scope scenario")
		}
	}

	itemID, err := ProfileItemID(boundary.Scope)
	if err != nil {
		return nil, nil, err
	}
	maxRevision := uint64(0)
	var active *RevisionRef
	for _, profile := range snapshot.Profiles {
		if profile.Meta.ItemID != itemID {
			return nil, nil, invalidDerived("Store snapshot contains a foreign logical profile item")
		}
		if profile.Meta.Revision > maxRevision {
			maxRevision = profile.Meta.Revision
		}
		if profile.Meta.State == RevisionStateActive {
			if active != nil {
				return nil, nil, invalidDerived("Store snapshot contains multiple active profile revisions")
			}
			ref := revisionRef(profile.Meta)
			active = &ref
		}
	}

	var supersedes *RevisionRef
	transitions := []RevisionTransition(nil)
	parents := append([]RevisionRef(nil), candidate.Atoms...)
	parents = append(parents, candidate.Scenarios...)
	if candidate.Supersedes != nil {
		if active == nil || *candidate.Supersedes != *active {
			return nil, nil, invalidDerived("profile supersession target is not the active profile revision")
		}
		target := *candidate.Supersedes
		supersedes = &target
		parents = appendUniqueRevisionRef(parents, target)
		transitions = append(transitions, RevisionTransition{
			Ref:  target,
			From: RevisionStateActive,
			To:   RevisionStateSuperseded,
		})
	}
	if active != nil && supersedes == nil {
		return nil, nil, invalidDerived("profile candidate would create a second active profile revision")
	}
	if active == nil && supersedes != nil {
		return nil, nil, invalidDerived("profile candidate supersedes a non-current profile revision")
	}
	if len(parents) > e.config.MaxSourcesPerRevision {
		return nil, nil, limitExceeded("profile provenance exceeds the configured limit")
	}
	summary := normalizeDerivedText(candidate.Summary)
	provenance := Provenance{ParentRevisions: parents}
	revisionID, err := DerivedRevisionID(
		boundary.Scope,
		itemID,
		operationID,
		[]string{summary},
		provenance,
		supersedes,
	)
	if err != nil {
		return nil, nil, err
	}
	profile := Profile{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindProfile,
			ItemID:        itemID,
			RevisionID:    revisionID,
			Revision:      maxRevision + 1,
			OperationID:   operationID,
			Scope:         boundary.Scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     boundary.OccurredAt,
			Supersedes:    supersedes,
		},
		Summary: summary,
	}
	if err := profile.Validate(); err != nil {
		return nil, nil, err
	}
	return []Profile{profile}, transitions, nil
}

func revisionRef(meta RevisionMeta) RevisionRef {
	return RevisionRef{ItemID: meta.ItemID, RevisionID: meta.RevisionID}
}

func appendUniqueRevisionRef(refs []RevisionRef, ref RevisionRef) []RevisionRef {
	for _, current := range refs {
		if current == ref {
			return refs
		}
	}
	return append(refs, ref)
}
