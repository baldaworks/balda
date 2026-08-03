package sessionmemory

import "context"

// ProcessingOperationID derives a stable idempotency key for one processing stage.
func ProcessingOperationID(stage OperationStage, exportID string) (string, error) {
	if err := stage.Validate(); err != nil {
		return "", err
	}
	if !isCanonicalID(exportID) {
		return "", invalidDerived("processing export id is required")
	}
	return derivedStableID("operation", string(stage), exportID), nil
}

// ProcessTurn extracts and atomically commits atoms for one completed raw turn.
func (e *Engine) ProcessTurn(ctx context.Context, turn Turn) (OperationOutcome, error) {
	if e == nil {
		return OperationOutcome{}, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if err := turn.Validate(); err != nil {
		return OperationOutcome{}, err
	}
	if turnTextExceeds(turn, e.config.MaxTurnTextBytes) {
		return OperationOutcome{}, limitExceeded("raw turn text exceeds the derived processing limit")
	}
	operationID, err := ProcessingOperationID(OperationStageAtoms, turn.ExportID)
	if err != nil {
		return OperationOutcome{}, err
	}
	lookup := OperationLookup{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Stage:         OperationStageAtoms,
		Scope:         turn.Scope,
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

	snapshot, err := e.store.LoadScope(ctx, turn.Scope)
	if err != nil {
		return OperationOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	snapshot = cloneScopeSnapshot(snapshot)
	if err := snapshot.Validate(e.config.MaxSnapshotItems); err != nil {
		return OperationOutcome{}, err
	}
	if snapshot.Scope != turn.Scope {
		return OperationOutcome{}, PermanentError(CodeScopeViolation, "Store snapshot scope does not match the turn", nil)
	}
	source := sourceRefFromTurn(turn)
	if err := validateNewSource(snapshot.Sources, source); err != nil {
		return OperationOutcome{}, err
	}

	request := AtomExtractionRequest{
		SchemaVersion: DerivedSchemaVersionV1,
		Turn:          cloneTurn(turn),
		View:          scopeViewFromSnapshot(snapshot),
	}
	candidates, err := e.atomExtractor.ExtractAtoms(ctx, request)
	if err != nil {
		return OperationOutcome{}, modelPortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if len(candidates) > e.config.MaxCandidateCount {
		return OperationOutcome{}, limitExceeded("atom extraction exceeds the candidate limit")
	}
	atoms, transitions, err := e.groundAtomCandidates(turn, operationID, source, snapshot, candidates)
	if err != nil {
		return OperationOutcome{}, err
	}
	turnCopy := cloneTurn(turn)
	commit := CommitRequest{
		SchemaVersion:        DerivedSchemaVersionV1,
		OperationID:          operationID,
		Stage:                OperationStageAtoms,
		Scope:                turn.Scope,
		ExpectedScopeVersion: snapshot.Version,
		Sources: []SourceRecord{{
			SchemaVersion: DerivedSchemaVersionV1,
			Ref:           source,
			State:         SourceStateActive,
			Turn:          &turnCopy,
		}},
		Atoms:       atoms,
		Transitions: transitions,
	}
	if err := commit.Validate(); err != nil {
		return OperationOutcome{}, err
	}
	outcome, err := e.store.Commit(ctx, cloneCommitRequest(commit))
	if err != nil {
		return OperationOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return OperationOutcome{}, err
	}
	if err := validateCommittedOutcome(commit, outcome); err != nil {
		return OperationOutcome{}, err
	}
	return cloneOperationOutcome(outcome), nil
}

func (e *Engine) groundAtomCandidates(
	turn Turn,
	operationID string,
	source SourceRef,
	snapshot ScopeSnapshot,
	candidates []AtomCandidate,
) ([]Atom, []RevisionTransition, error) {
	active := make(map[RevisionRef]Atom)
	maxRevision := make(map[string]uint64)
	activeItem := make(map[string]RevisionRef)
	for _, atom := range snapshot.Atoms {
		ref := RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}
		if atom.Meta.Revision > maxRevision[atom.Meta.ItemID] {
			maxRevision[atom.Meta.ItemID] = atom.Meta.Revision
		}
		if atom.Meta.State == RevisionStateActive {
			active[ref] = atom
			if _, exists := activeItem[atom.Meta.ItemID]; exists {
				return nil, nil, invalidDerived("Store snapshot contains multiple active revisions for one atom item")
			}
			activeItem[atom.Meta.ItemID] = ref
		}
	}

	atoms := make([]Atom, 0, len(candidates))
	transitions := make([]RevisionTransition, 0, len(candidates))
	createdItems := make(map[string]struct{}, len(candidates))
	transitioned := make(map[RevisionRef]struct{}, len(candidates))
	for _, candidate := range candidates {
		if len(candidate.Text) > e.config.MaxDerivedTextBytes {
			return nil, nil, limitExceeded("atom candidate text exceeds the configured limit")
		}
		if err := candidate.Validate(); err != nil {
			return nil, nil, err
		}
		text, err := validateDerivedText("atom candidate text", candidate.Text)
		if err != nil {
			return nil, nil, err
		}
		if len(text) > e.config.MaxDerivedTextBytes {
			return nil, nil, limitExceeded("atom candidate text exceeds the configured limit")
		}
		itemID, err := AtomItemID(turn.Scope, candidate.Category, text)
		if err != nil {
			return nil, nil, err
		}
		if _, exists := createdItems[itemID]; exists {
			return nil, nil, invalidDerived("atom extraction contains duplicate logical items")
		}
		createdItems[itemID] = struct{}{}

		var related *RevisionRef
		var supersedes *RevisionRef
		provenance := Provenance{RawSources: []SourceRef{source}}
		if candidate.Target != nil {
			target := *candidate.Target
			if _, ok := active[target]; !ok {
				return nil, nil, invalidDerived("atom candidate target is not an active same-scope atom")
			}
			if _, exists := transitioned[target]; exists {
				return nil, nil, invalidDerived("atom extraction targets one revision more than once")
			}
			targetCopy := target
			related = &targetCopy
			provenance.ParentRevisions = []RevisionRef{target}
			if candidate.Relation == CandidateRelationSupersede {
				supersedes = &targetCopy
				transitioned[target] = struct{}{}
				transitions = append(transitions, RevisionTransition{
					Ref:  target,
					From: RevisionStateActive,
					To:   RevisionStateSuperseded,
				})
			}
		}
		if prior, exists := activeItem[itemID]; exists {
			if supersedes == nil || prior != *supersedes {
				return nil, nil, invalidDerived("atom candidate would create a second active revision for one item")
			}
		}

		revision := maxRevision[itemID] + 1
		contentParts := []string{string(candidate.Category), text, string(candidate.Relation)}
		if related != nil {
			contentParts = append(contentParts, related.ItemID, related.RevisionID)
		}
		revisionID, err := DerivedRevisionID(turn.Scope, itemID, operationID, contentParts, provenance, supersedes)
		if err != nil {
			return nil, nil, err
		}
		atom := Atom{
			Meta: RevisionMeta{
				SchemaVersion: DerivedSchemaVersionV1,
				Kind:          DerivedKindAtom,
				ItemID:        itemID,
				RevisionID:    revisionID,
				Revision:      revision,
				OperationID:   operationID,
				Scope:         turn.Scope,
				State:         RevisionStateActive,
				Provenance:    provenance,
				CreatedAt:     turn.CompletedAt,
				Supersedes:    supersedes,
			},
			Category:        candidate.Category,
			Text:            text,
			Relation:        candidate.Relation,
			RelatedRevision: related,
		}
		if err := atom.Validate(); err != nil {
			return nil, nil, err
		}
		atoms = append(atoms, atom)
	}
	return atoms, transitions, nil
}

func validateNewSource(records []SourceRecord, source SourceRef) error {
	for _, record := range records {
		if record.Ref.ExportID != source.ExportID {
			continue
		}
		if record.Ref != source {
			return PermanentError(CodeConflict, "raw source export identity collides with another source", nil)
		}
		if record.State == SourceStateForgotten {
			return PermanentError(CodeForgotten, "raw source was forgotten", nil)
		}
		return PermanentError(CodeConflict, "raw source exists without its operation outcome", nil)
	}
	return nil
}

func validateCommittedOutcome(request CommitRequest, outcome OperationOutcome) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if outcome.OperationID != request.OperationID || outcome.Stage != request.Stage || outcome.Scope != request.Scope {
		return PermanentError(CodeScopeViolation, "Store commit outcome does not match the operation", nil)
	}
	if outcome.ScopeVersion != request.ExpectedScopeVersion+1 {
		return PermanentError(CodeConflict, "Store commit returned an unexpected scope version", nil)
	}
	if len(outcome.Revisions) != len(request.Atoms)+len(request.Scenarios)+len(request.Profiles) {
		return invalidDerived("Store commit outcome revision count does not match the request")
	}
	want := make(map[RevisionRef]struct{}, len(outcome.Revisions))
	for _, atom := range request.Atoms {
		want[RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}] = struct{}{}
	}
	for _, scenario := range request.Scenarios {
		want[RevisionRef{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID}] = struct{}{}
	}
	for _, profile := range request.Profiles {
		want[RevisionRef{ItemID: profile.Meta.ItemID, RevisionID: profile.Meta.RevisionID}] = struct{}{}
	}
	for _, ref := range outcome.Revisions {
		if _, ok := want[ref]; !ok {
			return invalidDerived("Store commit outcome contains an unknown revision")
		}
		delete(want, ref)
	}
	return nil
}

func turnTextExceeds(turn Turn, limit int) bool {
	remaining := limit
	for _, message := range turn.Messages {
		if len(message.Text) > remaining {
			return true
		}
		remaining -= len(message.Text)
	}
	return false
}
