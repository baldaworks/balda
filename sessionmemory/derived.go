package sessionmemory

import "time"

const (
	// DerivedSchemaVersionV1 identifies the first portable derived-memory schema.
	DerivedSchemaVersionV1 = "session-memory-derived/v1"

	// MaxDerivedTurnTextBytes bounds raw message text accepted for derivation.
	MaxDerivedTurnTextBytes = 256 * 1024
	// MaxCandidateCount bounds model candidates returned by one stage.
	MaxCandidateCount = 32
	// MaxSourcesPerRevision bounds direct and parent provenance references.
	MaxSourcesPerRevision = 256
	// MaxDerivedTextBytes bounds any individual derived text field.
	MaxDerivedTextBytes = 32 * 1024
	// MaxSnapshotItems bounds a same-scope view supplied to a model.
	MaxSnapshotItems = 2048
	// MaxTraceNodes bounds one provenance traversal.
	MaxTraceNodes = 2048
	// MaxSearchResponseBytes bounds one derived search response.
	MaxSearchResponseBytes = 256 * 1024
)

// DerivedKind identifies one derived-memory layer.
type DerivedKind string

const (
	// DerivedKindAtom is an independently searchable statement.
	DerivedKindAtom DerivedKind = "atom"
	// DerivedKindScenario is a topic or project context summary.
	DerivedKindScenario DerivedKind = "scenario"
	// DerivedKindProfile is a long-lived exact-scope synthesis.
	DerivedKindProfile DerivedKind = "profile"
)

// AtomCategory classifies the meaning of an atom.
type AtomCategory string

const (
	// AtomCategoryFact is an asserted factual statement.
	AtomCategoryFact AtomCategory = "fact"
	// AtomCategoryPreference is a stated preference.
	AtomCategoryPreference AtomCategory = "preference"
	// AtomCategoryConstraint is a durable requirement or restriction.
	AtomCategoryConstraint AtomCategory = "constraint"
	// AtomCategoryDecision is a decision or commitment.
	AtomCategoryDecision AtomCategory = "decision"
	// AtomCategoryEvent is a time-relevant occurrence.
	AtomCategoryEvent AtomCategory = "event"
)

// RevisionState identifies whether a derived revision participates in recall.
type RevisionState string

const (
	// RevisionStateActive participates in normal retrieval.
	RevisionStateActive RevisionState = "active"
	// RevisionStateSuperseded was replaced but remains in provenance history.
	RevisionStateSuperseded RevisionState = "superseded"
	// RevisionStateInvalidated cannot participate in normal retrieval.
	RevisionStateInvalidated RevisionState = "invalidated"
)

// SourceState identifies whether a raw source remains readable.
type SourceState string

const (
	// SourceStateActive may ground new and existing derived memory.
	SourceStateActive SourceState = "active"
	// SourceStateForgotten is an identity-only tombstone.
	SourceStateForgotten SourceState = "forgotten"
)

// CandidateRelation describes how an atom candidate relates to prior memory.
type CandidateRelation string

const (
	// CandidateRelationNew creates a new logical atom.
	CandidateRelationNew CandidateRelation = "new"
	// CandidateRelationCoexist preserves an explicit contradictory alternative.
	CandidateRelationCoexist CandidateRelation = "coexist"
	// CandidateRelationSupersede replaces one active atom revision.
	CandidateRelationSupersede CandidateRelation = "supersede"
)

// OperationStage identifies one independently idempotent processing stage.
type OperationStage string

const (
	// OperationStageAtoms extracts atoms from one raw turn.
	OperationStageAtoms OperationStage = "atoms"
	// OperationStageScenarios synthesizes scenarios at a session boundary.
	OperationStageScenarios OperationStage = "scenarios"
	// OperationStageProfile synthesizes the exact-scope profile.
	OperationStageProfile OperationStage = "profile"
)

// SourceRef identifies one immutable raw turn source.
type SourceRef struct {
	Scope        Scope  `json:"scope"`
	ExportID     string `json:"export_id"`
	SessionID    string `json:"session_id"`
	SourceTurnID string `json:"source_turn_id"`
}

// RevisionRef identifies one immutable derived revision.
type RevisionRef struct {
	ItemID     string `json:"item_id"`
	RevisionID string `json:"revision_id"`
}

// Provenance grounds a derived revision in raw sources and parent revisions.
type Provenance struct {
	RawSources      []SourceRef   `json:"raw_sources,omitempty"`
	ParentRevisions []RevisionRef `json:"parent_revisions,omitempty"`
}

// RevisionMeta contains immutable identity and lifecycle metadata.
type RevisionMeta struct {
	SchemaVersion string        `json:"schema_version"`
	Kind          DerivedKind   `json:"kind"`
	ItemID        string        `json:"item_id"`
	RevisionID    string        `json:"revision_id"`
	Revision      uint64        `json:"revision"`
	OperationID   string        `json:"operation_id"`
	Scope         Scope         `json:"scope"`
	State         RevisionState `json:"state"`
	Provenance    Provenance    `json:"provenance"`
	CreatedAt     time.Time     `json:"created_at"`
	Supersedes    *RevisionRef  `json:"supersedes,omitempty"`
}

// Atom is one independently searchable derived statement.
type Atom struct {
	Meta            RevisionMeta      `json:"meta"`
	Category        AtomCategory      `json:"category"`
	Text            string            `json:"text"`
	Relation        CandidateRelation `json:"relation"`
	RelatedRevision *RevisionRef      `json:"related_revision,omitempty"`
}

// Scenario is one topic or project context revision.
type Scenario struct {
	Meta     RevisionMeta `json:"meta"`
	TopicKey string       `json:"topic_key"`
	Title    string       `json:"title"`
	Summary  string       `json:"summary"`
}

// Profile is one long-lived exact-scope synthesis revision.
type Profile struct {
	Meta    RevisionMeta `json:"meta"`
	Summary string       `json:"summary"`
}

// AtomCandidate is untrusted model output for one proposed atom.
type AtomCandidate struct {
	Category AtomCategory      `json:"category"`
	Text     string            `json:"text"`
	Relation CandidateRelation `json:"relation"`
	Target   *RevisionRef      `json:"target,omitempty"`
}

// ScenarioCandidate is untrusted model output for one proposed scenario.
type ScenarioCandidate struct {
	TopicKey   string        `json:"topic_key"`
	Title      string        `json:"title"`
	Summary    string        `json:"summary"`
	Atoms      []RevisionRef `json:"atoms"`
	Supersedes *RevisionRef  `json:"supersedes,omitempty"`
}

// ProfileCandidate is untrusted model output for one proposed profile.
type ProfileCandidate struct {
	Summary    string        `json:"summary"`
	Atoms      []RevisionRef `json:"atoms,omitempty"`
	Scenarios  []RevisionRef `json:"scenarios,omitempty"`
	Supersedes *RevisionRef  `json:"supersedes,omitempty"`
}

// OperationOutcome is the durable result of one idempotent processing stage.
type OperationOutcome struct {
	SchemaVersion string         `json:"schema_version"`
	OperationID   string         `json:"operation_id"`
	Stage         OperationStage `json:"stage"`
	Scope         Scope          `json:"scope"`
	ScopeVersion  uint64         `json:"scope_version"`
	Revisions     []RevisionRef  `json:"revisions,omitempty"`
}

// BoundaryOutcome contains the independently durable scenario and profile stages.
// When profile synthesis fails, Scenarios remains populated for safe replay.
type BoundaryOutcome struct {
	SchemaVersion string           `json:"schema_version"`
	Scope         Scope            `json:"scope"`
	Scenarios     OperationOutcome `json:"scenarios"`
	Profile       OperationOutcome `json:"profile"`
}
