package sessionmemory

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestMigrateV1ScopeSnapshotPreservesGroundedRecordsAndPayloads(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	completedAt := time.Date(2026, time.August, 6, 2, 3, 4, 0, time.UTC)
	turn, err := NewTerminalTurn(scope, SessionRef{SessionID: "session-1", AgentSessionID: "agent-1"}, "turn-1", completedAt, "Я живу в Бишкеке", "Понял", TurnTerminalStatusSuccess)
	if err != nil {
		t.Fatalf("NewTerminalTurn() error = %v", err)
	}
	source := SourceRecord{SchemaVersion: DerivedSchemaVersionV1, Ref: sourceRefFromTurn(turn), State: SourceStateActive, Turn: &turn}
	itemID, err := AtomItemID(scope, AtomCategoryFact, "Я живу в Бишкеке")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	provenance := Provenance{RawSources: []SourceRef{source.Ref}}
	revisionID, err := DerivedRevisionID(scope, itemID, "legacy-operation", []string{string(AtomCategoryFact), "Я живу в Бишкеке", string(CandidateRelationNew)}, provenance, nil)
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
			OperationID:   "legacy-operation",
			Scope:         scope,
			State:         RevisionStateActive,
			Provenance:    provenance,
			CreatedAt:     completedAt,
		},
		Category: AtomCategoryFact,
		Text:     "Я живу в Бишкеке",
		Relation: CandidateRelationNew,
	}
	store := &processorTestStore{state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope}}
	outcome, err := MigrateV1ScopeSnapshot(context.Background(), store, ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       7,
		Sources:       []SourceRecord{source},
		Atoms:         []Atom{atom},
	}, CanonicalMigrationConfig{})
	if err != nil {
		t.Fatalf("MigrateV1ScopeSnapshot() error = %v", err)
	}
	if outcome.ScopeVersion != 1 || outcome.ChangeSeq != 1 || len(outcome.RevisionIDs) != 1 {
		t.Fatalf("migration outcome = %+v", outcome)
	}
	mutation := store.mutation
	if len(mutation.Sources) != 1 || len(mutation.Messages) != 2 || len(mutation.Items) != 1 || len(mutation.Revisions) != 1 || len(mutation.Lifecycle) != 1 || len(mutation.Heads) != 1 {
		t.Fatalf("migration mutation shape = %+v", mutation)
	}
	if len(mutation.Payloads) != 4 {
		t.Fatalf("migration payload count = %d, want source + messages + revision", len(mutation.Payloads))
	}
	for _, payload := range mutation.Payloads {
		if err := payload.Validate(); err != nil {
			t.Fatalf("migration payload %q invalid: %v", payload.Ref.ID, err)
		}
		if len(payload.Data) == 0 || payload.Ref.ByteSize != uint32(len(payload.Data)) {
			t.Fatalf("migration payload %q has invalid bytes: %+v", payload.Ref.ID, payload)
		}
	}
	if mutation.Revisions[0].Evidence[0].SourceID != mutation.Sources[0].SourceID {
		t.Fatalf("migration evidence source = %q, want %q", mutation.Revisions[0].Evidence[0].SourceID, mutation.Sources[0].SourceID)
	}
	if mutation.Operation.CommittedAt != completedAt {
		t.Fatalf("migration committed_at = %s, want %s", mutation.Operation.CommittedAt, completedAt)
	}
}

func TestMigrateV1ScopeSnapshotPreservesScenarioAndProfileLayers(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:layers", Kind: ScopeKindPersonal}
	completedAt := time.Date(2026, time.August, 6, 4, 5, 6, 0, time.UTC)
	turn, err := NewTerminalTurn(scope, SessionRef{SessionID: "session-layers", AgentSessionID: "agent-layers"}, "turn-layers", completedAt, "layer source", "ack", TurnTerminalStatusSuccess)
	if err != nil {
		t.Fatalf("NewTerminalTurn() error = %v", err)
	}
	sourceRef := sourceRefFromTurn(turn)
	provenance := Provenance{RawSources: []SourceRef{sourceRef}}
	atomItemID, err := AtomItemID(scope, AtomCategoryFact, "layer fact")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	atomRevisionID, err := DerivedRevisionID(scope, atomItemID, "legacy-atoms", []string{"fact", "layer fact", "new"}, provenance, nil)
	if err != nil {
		t.Fatalf("atom DerivedRevisionID() error = %v", err)
	}
	atom := Atom{Meta: RevisionMeta{SchemaVersion: DerivedSchemaVersionV1, Kind: DerivedKindAtom, ItemID: atomItemID, RevisionID: atomRevisionID, Revision: 1, OperationID: "legacy-atoms", Scope: scope, State: RevisionStateActive, Provenance: provenance, CreatedAt: completedAt}, Category: AtomCategoryFact, Text: "layer fact", Relation: CandidateRelationNew}
	scenarioItemID, err := ScenarioItemID(scope, "layers")
	if err != nil {
		t.Fatalf("ScenarioItemID() error = %v", err)
	}
	scenarioProvenance := Provenance{RawSources: []SourceRef{sourceRef}, ParentRevisions: []RevisionRef{{ItemID: atomItemID, RevisionID: atomRevisionID}}}
	scenarioRevisionID, err := DerivedRevisionID(scope, scenarioItemID, "legacy-scenarios", []string{"layers", "Layer title", "Layer summary"}, scenarioProvenance, nil)
	if err != nil {
		t.Fatalf("scenario DerivedRevisionID() error = %v", err)
	}
	scenario := Scenario{Meta: RevisionMeta{SchemaVersion: DerivedSchemaVersionV1, Kind: DerivedKindScenario, ItemID: scenarioItemID, RevisionID: scenarioRevisionID, Revision: 1, OperationID: "legacy-scenarios", Scope: scope, State: RevisionStateActive, Provenance: scenarioProvenance, CreatedAt: completedAt}, TopicKey: "layers", Title: "Layer title", Summary: "Layer summary"}
	profileItemID, err := ProfileItemID(scope)
	if err != nil {
		t.Fatalf("ProfileItemID() error = %v", err)
	}
	profileProvenance := Provenance{RawSources: []SourceRef{sourceRef}, ParentRevisions: []RevisionRef{{ItemID: scenarioItemID, RevisionID: scenarioRevisionID}}}
	profileRevisionID, err := DerivedRevisionID(scope, profileItemID, "legacy-profile", []string{"Layer profile"}, profileProvenance, nil)
	if err != nil {
		t.Fatalf("profile DerivedRevisionID() error = %v", err)
	}
	profile := Profile{Meta: RevisionMeta{SchemaVersion: DerivedSchemaVersionV1, Kind: DerivedKindProfile, ItemID: profileItemID, RevisionID: profileRevisionID, Revision: 1, OperationID: "legacy-profile", Scope: scope, State: RevisionStateActive, Provenance: profileProvenance, CreatedAt: completedAt}, Summary: "Layer profile"}
	store := &processorTestStore{state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope}}
	if _, err := MigrateV1ScopeSnapshot(context.Background(), store, ScopeSnapshot{SchemaVersion: DerivedSchemaVersionV1, Scope: scope, Version: 8, Sources: []SourceRecord{{SchemaVersion: DerivedSchemaVersionV1, Ref: sourceRef, State: SourceStateActive, Turn: &turn}}, Atoms: []Atom{atom}, Scenarios: []Scenario{scenario}, Profiles: []Profile{profile}}, CanonicalMigrationConfig{MaxMutationRecords: 128}); err != nil {
		t.Fatalf("MigrateV1ScopeSnapshot() error = %v", err)
	}
	if len(store.mutation.Items) != 3 || len(store.mutation.Revisions) != 3 || len(store.mutation.Heads) != 3 {
		t.Fatalf("layer migration shape: items=%d revisions=%d heads=%d", len(store.mutation.Items), len(store.mutation.Revisions), len(store.mutation.Heads))
	}
	compatKinds := make(map[DerivedKind]bool)
	for _, payload := range store.mutation.Payloads {
		var compatibility CanonicalCompatibilityPayload
		if err := json.Unmarshal(payload.Data, &compatibility); err == nil && compatibility.SchemaVersion == CanonicalCompatibilitySchemaVersion {
			compatKinds[compatibility.Kind] = true
		}
	}
	for _, kind := range []DerivedKind{DerivedKindAtom, DerivedKindScenario, DerivedKindProfile} {
		if !compatKinds[kind] {
			t.Fatalf("migration compatibility payload missing kind %q: %#v", kind, compatKinds)
		}
	}
}

func TestMigrateV1ScopeSnapshotFailsClosedForForgottenEvidence(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:1:0", Kind: ScopeKindPersonal}
	forgottenAt := time.Date(2026, time.August, 6, 2, 3, 4, 0, time.UTC)
	sourceRef := SourceRef{Scope: scope, ExportID: "export-1", SessionID: "session-1", SourceTurnID: "turn-1"}
	itemID, err := AtomItemID(scope, AtomCategoryFact, "forgotten fact")
	if err != nil {
		t.Fatalf("AtomItemID() error = %v", err)
	}
	provenance := Provenance{RawSources: []SourceRef{sourceRef}}
	revisionID, err := DerivedRevisionID(scope, itemID, "legacy-operation", []string{string(AtomCategoryFact), "forgotten fact", string(CandidateRelationNew)}, provenance, nil)
	if err != nil {
		t.Fatalf("DerivedRevisionID() error = %v", err)
	}
	store := &processorTestStore{state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope}}
	_, err = MigrateV1ScopeSnapshot(context.Background(), store, ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Sources:       []SourceRecord{{SchemaVersion: DerivedSchemaVersionV1, Ref: sourceRef, State: SourceStateForgotten, ForgottenAt: &forgottenAt}},
		Atoms: []Atom{{
			Meta: RevisionMeta{
				SchemaVersion: DerivedSchemaVersionV1,
				Kind:          DerivedKindAtom,
				ItemID:        itemID,
				RevisionID:    revisionID,
				Revision:      1,
				OperationID:   "legacy-operation",
				Scope:         scope,
				State:         RevisionStateActive,
				Provenance:    provenance,
				CreatedAt:     forgottenAt,
			},
			Category: AtomCategoryFact,
			Text:     "forgotten fact",
			Relation: CandidateRelationNew,
		}},
	}, CanonicalMigrationConfig{})
	if err == nil {
		t.Fatal("MigrateV1ScopeSnapshot() accepted evidence grounded in a forgotten source")
	}
	if code, _, ok := ClassifyError(err); !ok || code != CodeForgotten {
		t.Fatalf("migration error = %v, want forgotten error", err)
	}
	if len(store.mutation.Sources) != 0 || len(store.mutation.Revisions) != 0 || len(store.mutation.Payloads) != 0 {
		t.Fatalf("migration applied partial mutation: %+v", store.mutation)
	}
}

func TestMigrateV1ScopeSnapshotPreservesOperationOutcomes(t *testing.T) {
	t.Parallel()

	scope := Scope{Key: "telegram:operation-migration", Kind: ScopeKindPersonal}
	legacy := OperationOutcome{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   "legacy-operation-1",
		Stage:         OperationStageAtoms,
		Scope:         scope,
		ScopeVersion:  17,
		Revisions:     []RevisionRef{{ItemID: "legacy-item-1", RevisionID: "legacy-revision-1"}},
	}
	store := &processorTestStore{state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope}}
	if _, err := MigrateV1ScopeSnapshot(context.Background(), store, ScopeSnapshot{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         scope,
		Version:       17,
	}, CanonicalMigrationConfig{
		SkipSourceRecords: true,
		SkipAtomRecords:   true,
		OperationLimit:    1,
		LegacyOperations:  []OperationOutcome{legacy},
	}); err != nil {
		t.Fatalf("MigrateV1ScopeSnapshot(operation batch) error = %v", err)
	}
	if len(store.mutation.ImportedOperations) != 1 {
		t.Fatalf("imported operation count = %d, want 1", len(store.mutation.ImportedOperations))
	}
	imported := store.mutation.ImportedOperations[0]
	if imported.SchemaVersion != CanonicalImportedOperationSchemaVersion || imported.Outcome.OperationID != legacy.OperationID || imported.Outcome.ScopeVersion != legacy.ScopeVersion || len(imported.Outcome.Revisions) != 1 || imported.Outcome.Revisions[0] != legacy.Revisions[0] {
		t.Fatalf("imported operation = %+v, want exact legacy outcome", imported)
	}
}
