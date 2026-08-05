package sessionmemory

import (
	"strings"
	"testing"
	"time"
)

func TestDerivedEnumsFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
	}{
		{name: "kind", err: DerivedKind("unknown").Validate()},
		{name: "category", err: AtomCategory("unknown").Validate()},
		{name: "revision state", err: RevisionState("unknown").Validate()},
		{name: "source state", err: SourceState("unknown").Validate()},
		{name: "candidate relation", err: CandidateRelation("unknown").Validate()},
		{name: "operation stage", err: OperationStage("unknown").Validate()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertDerivedErrorCode(t, tt.err, CodeInvalidDerived)
		})
	}
}

func TestDerivedIDsAreStableAndOrderIndependent(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	firstItem, err := AtomItemID(scope, AtomCategoryDecision, "  ship   package ")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	secondItem, err := AtomItemID(scope, AtomCategoryDecision, "ship package")
	if err != nil {
		t.Fatalf("AtomItemID() normalized error = %v", err)
	}
	if firstItem != secondItem {
		t.Fatalf("AtomItemID() = %q and %q, want stable normalized identity", firstItem, secondItem)
	}
	const wantItemID = "session-memory-derived:v1:atom:db098eb947f8d2972875c9ac0fc04928ae3171bc7b1c64a767af9c31a2c7b745"
	if firstItem != wantItemID {
		t.Fatalf("AtomItemID() = %q, want golden %q", firstItem, wantItemID)
	}

	firstSource := derivedTestSource(scope, "export-1", "turn-1")
	secondSource := derivedTestSource(scope, "export-2", "turn-2")
	firstProvenance := Provenance{RawSources: []SourceRef{firstSource, secondSource}}
	secondProvenance := Provenance{RawSources: []SourceRef{secondSource, firstSource}}
	firstRevision, err := DerivedRevisionID(scope, firstItem, "operation-1", []string{"decision", "ship package"}, firstProvenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	secondRevision, err := DerivedRevisionID(scope, firstItem, "operation-1", []string{"decision", "ship package"}, secondProvenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() reordered error = %v", err)
	}
	if firstRevision != secondRevision {
		t.Fatalf("DerivedRevisionID() = %q and %q, want provenance-order independence", firstRevision, secondRevision)
	}
	const wantRevisionID = "session-memory-derived:v1:revision:4289b2b9b94e6dbaae4f34db054ae30ec69bda99f810ea3dac05ccb61bc683fe"
	if firstRevision != wantRevisionID {
		t.Fatalf("DerivedRevisionID() = %q, want golden %q", firstRevision, wantRevisionID)
	}
}

func TestDerivedValuesValidateStableIdentityAndProvenance(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	atom := derivedTestAtom(t, scope)
	if err := atom.Validate(); err != nil {
		t.Fatalf("Atom.Validate() error = %v", err)
	}

	scenarioItemID, err := ScenarioItemID(scope, "release")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	scenarioProvenance := Provenance{ParentRevisions: []RevisionRef{{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}}}
	scenarioRevisionID, err := DerivedRevisionID(
		scope,
		scenarioItemID,
		"operation-scenario",
		[]string{"release", "Release", "Ship the public memory package"},
		scenarioProvenance,
		nil,
	)
	if err != nil {
		t.Fatalf("scenario DerivedRevisionID() error = %v", err)
	}
	scenario := Scenario{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindScenario,
			ItemID:        scenarioItemID,
			RevisionID:    scenarioRevisionID,
			Revision:      1,
			OperationID:   "operation-scenario",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    scenarioProvenance,
			CreatedAt:     derivedTestTime(),
		},
		TopicKey: "release",
		Title:    "Release",
		Summary:  "Ship the public memory package",
	}
	if err := scenario.Validate(); err != nil {
		t.Fatalf("Scenario.Validate() error = %v", err)
	}

	profileItemID, err := ProfileItemID(scope)
	if err != nil {
		t.Fatalf("ProfileItemID() error = %v", err)
	}
	profileProvenance := Provenance{ParentRevisions: []RevisionRef{{ItemID: scenario.Meta.ItemID, RevisionID: scenario.Meta.RevisionID}}}
	profileRevisionID, err := DerivedRevisionID(
		scope,
		profileItemID,
		"operation-profile",
		[]string{"Prefers portable public packages"},
		profileProvenance,
		nil,
	)
	if err != nil {
		t.Fatalf("profile DerivedRevisionID() error = %v", err)
	}
	profile := Profile{
		Meta: RevisionMeta{
			SchemaVersion: DerivedSchemaVersionV1,
			Kind:          DerivedKindProfile,
			ItemID:        profileItemID,
			RevisionID:    profileRevisionID,
			Revision:      1,
			OperationID:   "operation-profile",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    profileProvenance,
			CreatedAt:     derivedTestTime(),
		},
		Summary: "Prefers portable public packages",
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("Profile.Validate() error = %v", err)
	}
}

func TestAtomRelationsPreserveCrossItemConflictHistory(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	prior := derivedTestAtom(t, scope)
	priorRef := RevisionRef{ItemID: prior.Meta.ItemID, RevisionID: prior.Meta.RevisionID}

	for _, relation := range []CandidateRelation{CandidateRelationCoexist, CandidateRelationSupersede} {
		t.Run(string(relation), func(t *testing.T) {
			text := "Do not ship the public memory package"
			itemID, err := AtomItemID(scope, AtomCategoryDecision, text)
			if err != nil {
				t.Fatalf("AtomItemID() error = %v", err)
			}
			provenance := Provenance{
				RawSources:      []SourceRef{derivedTestSource(scope, "export-2", "turn-2")},
				ParentRevisions: []RevisionRef{priorRef},
			}
			var supersedes *RevisionRef
			if relation == CandidateRelationSupersede {
				supersedes = &priorRef
			}
			revisionID, err := DerivedRevisionID(
				scope,
				itemID,
				"operation-conflict-"+string(relation),
				[]string{string(AtomCategoryDecision), text, string(relation), priorRef.ItemID, priorRef.RevisionID},
				provenance,
				supersedes,
			)
			if err != nil {
				t.Fatalf("DerivedRevisionID() error = %v", err)
			}
			atom := Atom{
				Meta: RevisionMeta{
					SchemaVersion: DerivedSchemaVersionV1,
					Kind:          DerivedKindAtom,
					ItemID:        itemID,
					RevisionID:    revisionID,
					Revision:      1,
					OperationID:   "operation-conflict-" + string(relation),
					Scope:         scope,
					State:         RevisionStateActive,
					Provenance:    provenance,
					CreatedAt:     derivedTestTime(),
					Supersedes:    supersedes,
				},
				Category:        AtomCategoryDecision,
				Text:            text,
				Relation:        relation,
				RelatedRevision: &priorRef,
			}
			if err := atom.Validate(); err != nil {
				t.Fatalf("Atom.Validate() error = %v", err)
			}
		})
	}
}

func TestProvenanceRejectsForeignDuplicateAndExcessiveReferences(t *testing.T) {
	t.Parallel()

	scope := derivedTestScope()
	foreign := Scope{Key: "telegram:-100:42", Kind: ScopeKindGroup}
	foreignErr := (Provenance{RawSources: []SourceRef{derivedTestSource(foreign, "export-1", "turn-1")}}).Validate(scope)
	assertDerivedErrorCode(t, foreignErr, CodeScopeViolation)

	source := derivedTestSource(scope, "export-1", "turn-1")
	duplicateErr := (Provenance{RawSources: []SourceRef{source, source}}).Validate(scope)
	assertDerivedErrorCode(t, duplicateErr, CodeInvalidDerived)

	parents := make([]RevisionRef, MaxSourcesPerRevision+1)
	for index := range parents {
		parents[index] = RevisionRef{ItemID: "item-" + strings.Repeat("x", index+1), RevisionID: "revision-1"}
	}
	limitErr := (Provenance{ParentRevisions: parents}).Validate(scope)
	assertDerivedErrorCode(t, limitErr, CodeLimitExceeded)
}

func TestDerivedStateTransitionsAreAppendOnly(t *testing.T) {
	t.Parallel()

	allowed := [][2]RevisionState{
		{"", RevisionStateActive},
		{RevisionStateActive, RevisionStateSuperseded},
		{RevisionStateActive, RevisionStateInvalidated},
		{RevisionStateSuperseded, RevisionStateInvalidated},
	}
	for _, transition := range allowed {
		if err := ValidateRevisionStateTransition(transition[0], transition[1]); err != nil {
			t.Fatalf("ValidateRevisionStateTransition(%q, %q) error = %v", transition[0], transition[1], err)
		}
	}
	disallowed := [][2]RevisionState{
		{RevisionStateActive, RevisionStateActive},
		{RevisionStateSuperseded, RevisionStateActive},
		{RevisionStateInvalidated, RevisionStateActive},
	}
	for _, transition := range disallowed {
		assertDerivedErrorCode(t, ValidateRevisionStateTransition(transition[0], transition[1]), CodeInvalidDerived)
	}

	if err := ValidateSourceStateTransition("", SourceStateActive); err != nil {
		t.Fatalf("new source transition error = %v", err)
	}
	if err := ValidateSourceStateTransition(SourceStateActive, SourceStateForgotten); err != nil {
		t.Fatalf("forget source transition error = %v", err)
	}
	assertDerivedErrorCode(t, ValidateSourceStateTransition(SourceStateForgotten, SourceStateActive), CodeInvalidDerived)
}

func TestModelCandidatesFailClosed(t *testing.T) {
	t.Parallel()

	ref := RevisionRef{ItemID: "item-1", RevisionID: "revision-1"}
	valid := []interface{ Validate() error }{
		AtomCandidate{Category: AtomCategoryFact, Text: "The project uses Go", Relation: CandidateRelationNew},
		AtomCandidate{Category: AtomCategoryDecision, Text: "Ship it", Relation: CandidateRelationSupersede, Target: &ref},
		AtomCandidate{Category: AtomCategoryDecision, Text: "Do not ship it", Relation: CandidateRelationCoexist, Target: &ref},
		ScenarioCandidate{TopicKey: "release", Title: "Release", Summary: "Release context", Atoms: []RevisionRef{ref}},
		ProfileCandidate{Disposition: ProfileDispositionUpsert, Summary: "Long-lived context", Scenarios: []RevisionRef{ref}},
	}
	for index, candidate := range valid {
		if err := candidate.Validate(); err != nil {
			t.Fatalf("valid candidate %d error = %v", index, err)
		}
	}

	invalid := []interface{ Validate() error }{
		AtomCandidate{Category: AtomCategoryFact, Text: "missing target", Relation: CandidateRelationSupersede},
		AtomCandidate{Category: AtomCategoryFact, Text: "missing target", Relation: CandidateRelationCoexist},
		AtomCandidate{Category: AtomCategoryFact, Text: "unexpected target", Relation: CandidateRelationNew, Target: &ref},
		ScenarioCandidate{TopicKey: "release", Title: "Release", Summary: "missing atoms"},
		ProfileCandidate{Disposition: ProfileDispositionUpsert, Summary: "missing parents"},
		ProfileCandidate{Disposition: ProfileDispositionUpsert, Summary: strings.Repeat("x", MaxDerivedTextBytes+1), Atoms: []RevisionRef{ref}},
		ProfileCandidate{Disposition: ProfileDispositionUpsert, Summary: strings.Repeat(" ", MaxDerivedTextBytes+1), Atoms: []RevisionRef{ref}},
	}
	for index, candidate := range invalid {
		err := candidate.Validate()
		if err == nil {
			t.Fatalf("invalid candidate %d error = nil", index)
		}
		code, _, ok := ClassifyError(err)
		if !ok || (code != CodeInvalidDerived && code != CodeLimitExceeded) {
			t.Fatalf("invalid candidate %d error = %v, code = %q", index, err, code)
		}
	}
}

func TestProcessingOperationIDUsesDerivationVersion(t *testing.T) {
	t.Parallel()

	legacy, err := ProcessingOperationID(OperationStageAtoms, "export-1")
	if err != nil {
		t.Fatalf("ProcessingOperationID() error = %v", err)
	}
	explicitLegacy, err := ProcessingOperationID(OperationStageAtoms, "export-1", LegacyDerivationRef())
	if err != nil {
		t.Fatalf("ProcessingOperationID() explicit legacy error = %v", err)
	}
	if legacy != explicitLegacy {
		t.Fatalf("legacy operation IDs differ: %q != %q", legacy, explicitLegacy)
	}
	versioned := DerivationRef{Pipeline: "pipeline-v2", Policy: "policy-v2", Prompt: "prompt-v2", Model: "model-v2"}
	got, err := ProcessingOperationID(OperationStageAtoms, "export-1", versioned)
	if err != nil {
		t.Fatalf("ProcessingOperationID() versioned error = %v", err)
	}
	if got == legacy {
		t.Fatal("versioned derivation reused the legacy operation ID")
	}
}

func TestProfileCandidateSkipIsStrictAndRevisionFree(t *testing.T) {
	t.Parallel()

	skip := ProfileCandidate{Disposition: ProfileDispositionSkip}
	if err := skip.Validate(); err != nil {
		t.Fatalf("skip.Validate() error = %v", err)
	}
	if err := (ProfileCandidate{Disposition: ProfileDispositionSkip, Summary: "must not write"}).Validate(); err == nil {
		t.Fatal("populated skip candidate validated")
	}
}

func TestDerivedValidationErrorsDoNotEchoContent(t *testing.T) {
	t.Parallel()

	secret := "secret-model-output"
	err := (AtomCandidate{
		Category: AtomCategoryFact,
		Text:     strings.Repeat(secret, MaxDerivedTextBytes),
		Relation: CandidateRelationNew,
	}).Validate()
	if err == nil {
		t.Fatal("Validate() error = nil, want limit error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("validation error exposed rejected content: %v", err)
	}
}

func derivedTestAtom(t *testing.T, scope Scope) Atom {
	t.Helper()

	itemID, err := AtomItemID(scope, AtomCategoryDecision, "Ship the public memory package")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	provenance := Provenance{RawSources: []SourceRef{derivedTestSource(scope, "export-1", "turn-1")}}
	revisionID, err := DerivedRevisionID(
		scope,
		itemID,
		"operation-atom",
		[]string{string(AtomCategoryDecision), "Ship the public memory package", string(CandidateRelationNew)},
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
			OperationID:   "operation-atom",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     derivedTestTime(),
		},
		Category: AtomCategoryDecision,
		Text:     "Ship the public memory package",
		Relation: CandidateRelationNew,
	}
}

func derivedTestScope() Scope {
	return Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
}

func derivedTestSource(scope Scope, exportID, sourceTurnID string) SourceRef {
	return SourceRef{
		Scope:        scope,
		ExportID:     exportID,
		SessionID:    "session-1",
		SourceTurnID: sourceTurnID,
	}
}

func derivedTestTime() time.Time {
	return time.Date(2026, time.August, 3, 4, 5, 6, 0, time.UTC)
}

func assertDerivedErrorCode(t *testing.T, err error, want ErrorCode) {
	t.Helper()
	code, class, ok := ClassifyError(err)
	if !ok || code != want || class != ErrorClassPermanent {
		t.Fatalf("ClassifyError(%v) = %q, %q, %v; want %q, permanent, true", err, code, class, ok, want)
	}
}
