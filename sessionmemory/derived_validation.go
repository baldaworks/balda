package sessionmemory

import "strings"

// Validate verifies a supported derived-memory kind.
func (k DerivedKind) Validate() error {
	switch k {
	case DerivedKindAtom, DerivedKindScenario, DerivedKindProfile:
		return nil
	default:
		return invalidDerived("unsupported derived kind")
	}
}

// Validate verifies a supported atom category.
func (c AtomCategory) Validate() error {
	switch c {
	case AtomCategoryFact, AtomCategoryPreference, AtomCategoryConstraint, AtomCategoryDecision, AtomCategoryEvent:
		return nil
	default:
		return invalidDerived("unsupported atom category")
	}
}

// Validate verifies a supported derived revision state.
func (s RevisionState) Validate() error {
	switch s {
	case RevisionStateActive, RevisionStateSuperseded, RevisionStateInvalidated:
		return nil
	default:
		return invalidDerived("unsupported revision state")
	}
}

// Validate verifies a supported raw source state.
func (s SourceState) Validate() error {
	switch s {
	case SourceStateActive, SourceStateForgotten:
		return nil
	default:
		return invalidDerived("unsupported source state")
	}
}

// Validate verifies a supported atom candidate relationship.
func (r CandidateRelation) Validate() error {
	switch r {
	case CandidateRelationNew, CandidateRelationCoexist, CandidateRelationSupersede:
		return nil
	default:
		return invalidDerived("unsupported candidate relation")
	}
}

// Validate verifies a supported idempotent processing stage.
func (s OperationStage) Validate() error {
	switch s {
	case OperationStageAtoms, OperationStageScenarios, OperationStageProfile:
		return nil
	default:
		return invalidDerived("unsupported operation stage")
	}
}

// Validate verifies a complete, content-free derivation identity.
func (r DerivationRef) Validate() error {
	for _, field := range []string{r.Pipeline, r.Policy, r.Prompt, r.Model} {
		if !isCanonicalID(field) {
			return invalidDerived("derivation fingerprint is required")
		}
	}
	return nil
}

// Validate verifies a supported profile synthesis disposition.
func (d ProfileDisposition) Validate() error {
	switch d {
	case ProfileDispositionSkip, ProfileDispositionUpsert:
		return nil
	default:
		return invalidDerived("unsupported profile candidate disposition")
	}
}

// Validate verifies a complete exact-scope raw source identity.
func (r SourceRef) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(r.ExportID) {
		return invalidDerived("source export id is required")
	}
	if !isCanonicalID(r.SessionID) {
		return invalidDerived("source session id is required")
	}
	if !isCanonicalID(r.SourceTurnID) {
		return invalidDerived("source turn id is required")
	}
	return nil
}

// Validate verifies an immutable derived revision identity.
func (r RevisionRef) Validate() error {
	if !isCanonicalID(r.ItemID) {
		return invalidDerived("derived item id is required")
	}
	if !isCanonicalID(r.RevisionID) {
		return invalidDerived("derived revision id is required")
	}
	return nil
}

// Validate verifies non-empty, bounded, duplicate-free same-scope provenance.
func (p Provenance) Validate(scope Scope) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	total := len(p.RawSources) + len(p.ParentRevisions)
	if total == 0 {
		return invalidDerived("derived provenance is required")
	}
	if total > MaxSourcesPerRevision {
		return limitExceeded("derived provenance exceeds the reference limit")
	}

	rawSeen := make(map[string]struct{}, len(p.RawSources))
	for _, source := range p.RawSources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != scope {
			return PermanentError(CodeScopeViolation, "raw source scope does not match the derived scope", nil)
		}
		key := source.Scope.Key + "\x00" + source.ExportID
		if _, ok := rawSeen[key]; ok {
			return invalidDerived("duplicate raw source reference")
		}
		rawSeen[key] = struct{}{}
	}

	parentSeen := make(map[RevisionRef]struct{}, len(p.ParentRevisions))
	for _, parent := range p.ParentRevisions {
		if err := parent.Validate(); err != nil {
			return err
		}
		if _, ok := parentSeen[parent]; ok {
			return invalidDerived("duplicate parent revision reference")
		}
		parentSeen[parent] = struct{}{}
	}
	return nil
}

// Validate verifies a complete atom revision and its deterministic identities.
func (a Atom) Validate() error {
	if err := a.Category.Validate(); err != nil {
		return err
	}
	text, err := validateDerivedText("atom text", a.Text)
	if err != nil {
		return err
	}
	if text != a.Text {
		return invalidDerived("atom text must be normalized")
	}
	if err := a.Relation.Validate(); err != nil {
		return err
	}
	if err := validateAtomRelation(a); err != nil {
		return err
	}
	itemID, err := AtomItemID(a.Meta.Scope, a.Category, a.Text)
	if err != nil {
		return err
	}
	contentParts := []string{string(a.Category), a.Text, string(a.Relation)}
	if a.RelatedRevision != nil {
		contentParts = append(contentParts, a.RelatedRevision.ItemID, a.RelatedRevision.RevisionID)
	}
	return validateRevisionMeta(a.Meta, DerivedKindAtom, itemID, contentParts, true)
}

// Validate verifies a complete scenario revision and its deterministic identities.
func (s Scenario) Validate() error {
	topicKey, err := validateDerivedKey("scenario topic key", s.TopicKey)
	if err != nil {
		return err
	}
	if topicKey != s.TopicKey {
		return invalidDerived("scenario topic key must be normalized")
	}
	title, err := validateDerivedText("scenario title", s.Title)
	if err != nil {
		return err
	}
	if title != s.Title {
		return invalidDerived("scenario title must be normalized")
	}
	summary, err := validateDerivedText("scenario summary", s.Summary)
	if err != nil {
		return err
	}
	if summary != s.Summary {
		return invalidDerived("scenario summary must be normalized")
	}
	itemID, err := ScenarioItemID(s.Meta.Scope, s.TopicKey)
	if err != nil {
		return err
	}
	return validateRevisionMeta(s.Meta, DerivedKindScenario, itemID, []string{s.TopicKey, s.Title, s.Summary}, false)
}

// Validate verifies a complete profile revision and its deterministic identities.
func (p Profile) Validate() error {
	summary, err := validateDerivedText("profile summary", p.Summary)
	if err != nil {
		return err
	}
	if summary != p.Summary {
		return invalidDerived("profile summary must be normalized")
	}
	itemID, err := ProfileItemID(p.Meta.Scope)
	if err != nil {
		return err
	}
	return validateRevisionMeta(p.Meta, DerivedKindProfile, itemID, []string{p.Summary}, false)
}

// Validate verifies syntactically grounded atom candidate output.
func (c AtomCandidate) Validate() error {
	if err := c.Category.Validate(); err != nil {
		return err
	}
	if _, err := validateDerivedText("atom candidate text", c.Text); err != nil {
		return err
	}
	if err := c.Relation.Validate(); err != nil {
		return err
	}
	switch c.Relation {
	case CandidateRelationNew:
		if c.Target != nil {
			return invalidDerived("new atom candidate cannot have a target")
		}
	case CandidateRelationCoexist, CandidateRelationSupersede:
		if c.Target == nil {
			return invalidDerived("related atom candidate requires a target")
		}
		if err := c.Target.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate verifies syntactically grounded scenario candidate output.
func (c ScenarioCandidate) Validate() error {
	if _, err := validateDerivedKey("scenario candidate topic key", c.TopicKey); err != nil {
		return err
	}
	if _, err := validateDerivedText("scenario candidate title", c.Title); err != nil {
		return err
	}
	if _, err := validateDerivedText("scenario candidate summary", c.Summary); err != nil {
		return err
	}
	if len(c.Atoms) == 0 {
		return invalidDerived("scenario candidate requires an atom reference")
	}
	if err := validateRevisionRefs(c.Atoms); err != nil {
		return err
	}
	if c.Supersedes != nil {
		if err := c.Supersedes.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate verifies syntactically grounded profile candidate output.
func (c ProfileCandidate) Validate() error {
	if err := c.Disposition.Validate(); err != nil {
		return err
	}
	if c.Disposition == ProfileDispositionSkip {
		if c.Summary != "" || len(c.Atoms) != 0 || len(c.Scenarios) != 0 || c.Supersedes != nil {
			return invalidDerived("skipped profile candidate cannot contain a revision")
		}
		return nil
	}
	if _, err := validateDerivedText("profile candidate summary", c.Summary); err != nil {
		return err
	}
	if len(c.Atoms)+len(c.Scenarios) == 0 {
		return invalidDerived("profile candidate requires a parent revision")
	}
	if len(c.Atoms)+len(c.Scenarios) > MaxSourcesPerRevision {
		return limitExceeded("profile candidate exceeds the parent reference limit")
	}
	if err := validateRevisionRefs(c.Atoms); err != nil {
		return err
	}
	if err := validateRevisionRefs(c.Scenarios); err != nil {
		return err
	}
	if c.Supersedes != nil {
		if err := c.Supersedes.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// Validate verifies an idempotent processing outcome.
func (o OperationOutcome) Validate() error {
	if o.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported operation outcome schema version")
	}
	if !isCanonicalID(o.OperationID) {
		return invalidDerived("operation id is required")
	}
	if err := o.Stage.Validate(); err != nil {
		return err
	}
	if err := o.Scope.Validate(); err != nil {
		return err
	}
	if o.ScopeVersion == 0 {
		return invalidDerived("scope version is required")
	}
	if len(o.Revisions) > MaxCandidateCount {
		return limitExceeded("operation outcome exceeds the revision limit")
	}
	return validateRevisionRefs(o.Revisions)
}

// Validate verifies both independently committed boundary stages.
func (o BoundaryOutcome) Validate() error {
	if o.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported boundary outcome schema version")
	}
	if err := o.Scope.Validate(); err != nil {
		return err
	}
	if err := o.Scenarios.Validate(); err != nil {
		return err
	}
	if err := o.Profile.Validate(); err != nil {
		return err
	}
	if o.Scenarios.Scope != o.Scope || o.Profile.Scope != o.Scope {
		return PermanentError(CodeScopeViolation, "boundary outcome stage scope does not match the boundary", nil)
	}
	if o.Scenarios.Stage != OperationStageScenarios || o.Profile.Stage != OperationStageProfile {
		return invalidDerived("boundary outcome contains the wrong processing stages")
	}
	if o.Profile.ScopeVersion <= o.Scenarios.ScopeVersion {
		return invalidDerived("profile stage must commit after the scenario stage")
	}
	return nil
}

// ValidateRevisionStateTransition verifies an append-only revision lifecycle.
func ValidateRevisionStateTransition(from, to RevisionState) error {
	if to.Validate() != nil {
		return invalidDerived("unsupported target revision state")
	}
	if from == "" && to == RevisionStateActive {
		return nil
	}
	if err := from.Validate(); err != nil {
		return err
	}
	if from == RevisionStateActive && (to == RevisionStateSuperseded || to == RevisionStateInvalidated) {
		return nil
	}
	if from == RevisionStateSuperseded && to == RevisionStateInvalidated {
		return nil
	}
	return invalidDerived("revision state transition is not allowed")
}

// ValidateSourceStateTransition verifies an append-only raw source lifecycle.
func ValidateSourceStateTransition(from, to SourceState) error {
	if to.Validate() != nil {
		return invalidDerived("unsupported target source state")
	}
	if from == "" && to == SourceStateActive {
		return nil
	}
	if err := from.Validate(); err != nil {
		return err
	}
	if from == SourceStateActive && to == SourceStateForgotten {
		return nil
	}
	return invalidDerived("source state transition is not allowed")
}

func validateRevisionMeta(
	meta RevisionMeta,
	kind DerivedKind,
	itemID string,
	contentParts []string,
	allowCrossItemSupersession bool,
) error {
	if meta.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported derived revision schema version")
	}
	if meta.Kind != kind {
		return invalidDerived("derived revision kind does not match its value")
	}
	if meta.ItemID != itemID {
		return invalidDerived("derived item id does not match its stable identity")
	}
	if meta.Revision == 0 {
		return invalidDerived("derived revision number is required")
	}
	if !isCanonicalID(meta.OperationID) {
		return invalidDerived("derived operation id is required")
	}
	if err := meta.Scope.Validate(); err != nil {
		return err
	}
	if err := meta.State.Validate(); err != nil {
		return err
	}
	if err := meta.Provenance.Validate(meta.Scope); err != nil {
		return err
	}
	if meta.CreatedAt.IsZero() {
		return invalidDerived("derived creation time is required")
	}
	if meta.Supersedes != nil {
		if err := meta.Supersedes.Validate(); err != nil {
			return err
		}
		sameItem := meta.Supersedes.ItemID == meta.ItemID
		if sameItem && meta.Revision < 2 {
			return invalidDerived("superseding revision number must be greater than one")
		}
		if !sameItem && !allowCrossItemSupersession {
			return invalidDerived("superseded revision must belong to the same item")
		}
	}
	wantRevisionID, err := DerivedRevisionID(meta.Scope, meta.ItemID, meta.OperationID, contentParts, meta.Provenance, meta.Supersedes)
	if err != nil {
		return err
	}
	if meta.RevisionID != wantRevisionID {
		return invalidDerived("derived revision id does not match its stable identity")
	}
	return nil
}

func validateAtomRelation(atom Atom) error {
	switch atom.Relation {
	case CandidateRelationNew:
		if atom.RelatedRevision != nil || atom.Meta.Supersedes != nil {
			return invalidDerived("new atom cannot relate to a prior revision")
		}
	case CandidateRelationCoexist:
		if atom.RelatedRevision == nil {
			return invalidDerived("coexisting atom requires a related revision")
		}
		if atom.Meta.Supersedes != nil {
			return invalidDerived("coexisting atom cannot supersede a revision")
		}
	case CandidateRelationSupersede:
		if atom.RelatedRevision == nil || atom.Meta.Supersedes == nil {
			return invalidDerived("superseding atom requires a related revision")
		}
		if *atom.RelatedRevision != *atom.Meta.Supersedes {
			return invalidDerived("superseding atom relation does not match revision metadata")
		}
	}
	if atom.RelatedRevision != nil {
		if err := atom.RelatedRevision.Validate(); err != nil {
			return err
		}
		if !containsRevisionRef(atom.Meta.Provenance.ParentRevisions, *atom.RelatedRevision) {
			return invalidDerived("related atom revision must be retained in provenance")
		}
	}
	return nil
}

func containsRevisionRef(refs []RevisionRef, want RevisionRef) bool {
	for _, ref := range refs {
		if ref == want {
			return true
		}
	}
	return false
}

func validateRevisionRefs(refs []RevisionRef) error {
	if len(refs) > MaxSourcesPerRevision {
		return limitExceeded("derived parent references exceed the limit")
	}
	seen := make(map[RevisionRef]struct{}, len(refs))
	for _, ref := range refs {
		if err := ref.Validate(); err != nil {
			return err
		}
		if _, ok := seen[ref]; ok {
			return invalidDerived("duplicate derived revision reference")
		}
		seen[ref] = struct{}{}
	}
	return nil
}

func validateDerivedKey(name, value string) (string, error) {
	if len(value) > MaxSearchQueryBytes {
		return "", limitExceeded(name + " exceeds the size limit")
	}
	normalized := normalizeDerivedText(value)
	if normalized == "" {
		return "", invalidDerived(name + " is required")
	}
	return normalized, nil
}

func validateDerivedText(name, value string) (string, error) {
	if len(value) > MaxDerivedTextBytes {
		return "", limitExceeded(name + " exceeds the size limit")
	}
	normalized := normalizeDerivedText(value)
	if normalized == "" {
		return "", invalidDerived(name + " is required")
	}
	return normalized, nil
}

func normalizeDerivedText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func invalidDerived(message string) error {
	return PermanentError(CodeInvalidDerived, message, nil)
}

func limitExceeded(message string) error {
	return PermanentError(CodeLimitExceeded, message, nil)
}
