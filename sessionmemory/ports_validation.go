package sessionmemory

// Validate verifies an active source or an identity-only forgotten tombstone.
func (s SourceRecord) Validate() error {
	if s.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported source record schema version")
	}
	if err := s.Ref.Validate(); err != nil {
		return err
	}
	if err := s.State.Validate(); err != nil {
		return err
	}
	switch s.State {
	case SourceStateActive:
		if s.Turn == nil {
			return invalidDerived("active source requires its raw turn")
		}
		if s.ForgottenAt != nil {
			return invalidDerived("active source cannot have a forgotten timestamp")
		}
		if err := s.Turn.Validate(); err != nil {
			return err
		}
		if sourceRefFromTurn(*s.Turn) != s.Ref {
			return invalidDerived("source reference does not match its raw turn")
		}
	case SourceStateForgotten:
		if s.Turn != nil {
			return invalidDerived("forgotten source cannot retain raw turn content")
		}
		if s.ForgottenAt == nil || s.ForgottenAt.IsZero() {
			return invalidDerived("forgotten source requires its forgotten timestamp")
		}
	}
	return nil
}

// Validate verifies one bounded exact-scope Store snapshot.
func (s ScopeSnapshot) Validate(maxItems int) error {
	if s.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported scope snapshot schema version")
	}
	if err := s.Scope.Validate(); err != nil {
		return err
	}
	if maxItems < 1 || maxItems > MaxSnapshotItems {
		return limitExceeded("snapshot validation limit is outside the allowed range")
	}
	if len(s.Sources)+len(s.Atoms)+len(s.Scenarios)+len(s.Profiles) > maxItems {
		return limitExceeded("scope snapshot exceeds the item limit")
	}
	sourceSeen := make(map[string]struct{}, len(s.Sources))
	for _, source := range s.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Ref.Scope != s.Scope {
			return PermanentError(CodeScopeViolation, "source record scope does not match snapshot scope", nil)
		}
		if _, ok := sourceSeen[source.Ref.ExportID]; ok {
			return invalidDerived("scope snapshot contains duplicate source identity")
		}
		sourceSeen[source.Ref.ExportID] = struct{}{}
	}
	revisionSeen := make(map[string]struct{}, len(s.Atoms)+len(s.Scenarios)+len(s.Profiles))
	for _, atom := range s.Atoms {
		if err := atom.Validate(); err != nil {
			return err
		}
		if err := validateSnapshotRevision(s.Scope, atom.Meta, revisionSeen); err != nil {
			return err
		}
	}
	for _, scenario := range s.Scenarios {
		if err := scenario.Validate(); err != nil {
			return err
		}
		if err := validateSnapshotRevision(s.Scope, scenario.Meta, revisionSeen); err != nil {
			return err
		}
	}
	for _, profile := range s.Profiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if err := validateSnapshotRevision(s.Scope, profile.Meta, revisionSeen); err != nil {
			return err
		}
	}
	return nil
}

// Validate verifies a bounded same-scope model view.
func (v ScopeView) Validate(maxItems int) error {
	return (ScopeSnapshot{
		SchemaVersion: v.SchemaVersion,
		Scope:         v.Scope,
		Version:       v.Version,
		Atoms:         v.Atoms,
		Scenarios:     v.Scenarios,
		Profiles:      v.Profiles,
	}).Validate(maxItems)
}

// Validate verifies an operation lookup identity.
func (l OperationLookup) Validate() error {
	if l.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported operation lookup schema version")
	}
	if !isCanonicalID(l.OperationID) {
		return invalidDerived("operation lookup id is required")
	}
	if err := l.Stage.Validate(); err != nil {
		return err
	}
	return l.Scope.Validate()
}

// Validate verifies an operation lookup result against its request.
func (r OperationLookupResult) Validate(lookup OperationLookup) error {
	if err := lookup.Validate(); err != nil {
		return err
	}
	if !r.Found {
		if !operationOutcomeIsZero(r.Outcome) {
			return invalidDerived("missing operation lookup cannot contain an outcome")
		}
		return nil
	}
	if err := r.Outcome.Validate(); err != nil {
		return err
	}
	if r.Outcome.OperationID != lookup.OperationID || r.Outcome.Stage != lookup.Stage || r.Outcome.Scope != lookup.Scope {
		return PermanentError(CodeScopeViolation, "operation lookup outcome does not match the request", nil)
	}
	return nil
}

func operationOutcomeIsZero(outcome OperationOutcome) bool {
	return outcome.SchemaVersion == "" && outcome.OperationID == "" && outcome.Stage == "" &&
		outcome.Scope == (Scope{}) && outcome.ScopeVersion == 0 && len(outcome.Revisions) == 0
}

// Validate verifies one append-only revision state transition.
func (t RevisionTransition) Validate() error {
	if err := t.Ref.Validate(); err != nil {
		return err
	}
	return ValidateRevisionStateTransition(t.From, t.To)
}

// Validate verifies one bounded atomic Store commit.
func (r CommitRequest) Validate() error {
	if r.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported commit request schema version")
	}
	if !isCanonicalID(r.OperationID) {
		return invalidDerived("commit operation id is required")
	}
	if err := r.Stage.Validate(); err != nil {
		return err
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if len(r.Sources) > 1 {
		return limitExceeded("commit contains too many source writes")
	}
	if len(r.Atoms)+len(r.Scenarios)+len(r.Profiles) > MaxCandidateCount {
		return limitExceeded("commit contains too many derived revisions")
	}
	switch r.Stage {
	case OperationStageAtoms:
		if len(r.Scenarios) != 0 || len(r.Profiles) != 0 {
			return invalidDerived("atom-stage commit cannot contain aggregate revisions")
		}
	case OperationStageScenarios:
		if len(r.Sources) != 0 || len(r.Atoms) != 0 || len(r.Profiles) != 0 {
			return invalidDerived("scenario-stage commit contains unsupported writes")
		}
	case OperationStageProfile:
		if len(r.Sources) != 0 || len(r.Atoms) != 0 || len(r.Scenarios) != 0 || len(r.Profiles) > 1 {
			return invalidDerived("profile-stage commit contains unsupported writes")
		}
	}
	for _, source := range r.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Ref.Scope != r.Scope {
			return PermanentError(CodeScopeViolation, "commit source scope does not match the operation", nil)
		}
	}
	created := make(map[RevisionRef]struct{}, len(r.Atoms)+len(r.Scenarios)+len(r.Profiles))
	for _, atom := range r.Atoms {
		if err := atom.Validate(); err != nil {
			return err
		}
		if err := validateCommitRevision(r, atom.Meta, created); err != nil {
			return err
		}
	}
	for _, scenario := range r.Scenarios {
		if err := scenario.Validate(); err != nil {
			return err
		}
		if err := validateCommitRevision(r, scenario.Meta, created); err != nil {
			return err
		}
	}
	for _, profile := range r.Profiles {
		if err := profile.Validate(); err != nil {
			return err
		}
		if err := validateCommitRevision(r, profile.Meta, created); err != nil {
			return err
		}
	}
	transitionSeen := make(map[RevisionRef]struct{}, len(r.Transitions))
	for _, transition := range r.Transitions {
		if err := transition.Validate(); err != nil {
			return err
		}
		if _, ok := transitionSeen[transition.Ref]; ok {
			return invalidDerived("commit contains duplicate revision transition")
		}
		transitionSeen[transition.Ref] = struct{}{}
	}
	for _, atom := range r.Atoms {
		if atom.Meta.Supersedes == nil {
			continue
		}
		transition, ok := transitionForRef(r.Transitions, *atom.Meta.Supersedes)
		if !ok || transition.From != RevisionStateActive || transition.To != RevisionStateSuperseded {
			return invalidDerived("superseding atom requires its active-to-superseded transition")
		}
	}
	return nil
}

func transitionForRef(transitions []RevisionTransition, ref RevisionRef) (RevisionTransition, bool) {
	for _, transition := range transitions {
		if transition.Ref == ref {
			return transition, true
		}
	}
	return RevisionTransition{}, false
}

func validateSnapshotRevision(scope Scope, meta RevisionMeta, seen map[string]struct{}) error {
	if meta.Scope != scope {
		return PermanentError(CodeScopeViolation, "derived revision scope does not match snapshot scope", nil)
	}
	if _, ok := seen[meta.RevisionID]; ok {
		return invalidDerived("scope snapshot contains duplicate revision identity")
	}
	seen[meta.RevisionID] = struct{}{}
	return nil
}

func validateCommitRevision(request CommitRequest, meta RevisionMeta, seen map[RevisionRef]struct{}) error {
	if meta.Scope != request.Scope {
		return PermanentError(CodeScopeViolation, "commit revision scope does not match the operation", nil)
	}
	if meta.OperationID != request.OperationID {
		return invalidDerived("commit revision operation does not match the request")
	}
	if meta.State != RevisionStateActive {
		return invalidDerived("new commit revision must be active")
	}
	ref := RevisionRef{ItemID: meta.ItemID, RevisionID: meta.RevisionID}
	if _, ok := seen[ref]; ok {
		return invalidDerived("commit contains duplicate derived revision")
	}
	seen[ref] = struct{}{}
	return nil
}

func sourceRefFromTurn(turn Turn) SourceRef {
	return SourceRef{
		Scope:        turn.Scope,
		ExportID:     turn.ExportID,
		SessionID:    turn.Session.SessionID,
		SourceTurnID: turn.SourceTurnID,
	}
}
