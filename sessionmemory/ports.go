package sessionmemory

import (
	"context"
	"time"
)

// SourceRecord stores either an active raw turn or an identity-only tombstone.
type SourceRecord struct {
	SchemaVersion string      `json:"schema_version"`
	Ref           SourceRef   `json:"ref"`
	State         SourceState `json:"state"`
	Turn          *Turn       `json:"turn,omitempty"`
	ForgottenAt   *time.Time  `json:"forgotten_at,omitempty"`
}

// ScopeSnapshot is one versioned exact-scope persistence view.
type ScopeSnapshot struct {
	SchemaVersion string         `json:"schema_version"`
	Scope         Scope          `json:"scope"`
	Version       uint64         `json:"version"`
	Sources       []SourceRecord `json:"sources,omitempty"`
	Atoms         []Atom         `json:"atoms,omitempty"`
	Scenarios     []Scenario     `json:"scenarios,omitempty"`
	Profiles      []Profile      `json:"profiles,omitempty"`
}

// ScopeView is bounded same-scope derived context supplied to a model port.
type ScopeView struct {
	SchemaVersion string     `json:"schema_version"`
	Scope         Scope      `json:"scope"`
	Version       uint64     `json:"version"`
	Atoms         []Atom     `json:"atoms,omitempty"`
	Scenarios     []Scenario `json:"scenarios,omitempty"`
	Profiles      []Profile  `json:"profiles,omitempty"`
}

// OperationLookup asks whether one exact-scope stage already committed.
type OperationLookup struct {
	SchemaVersion string         `json:"schema_version"`
	OperationID   string         `json:"operation_id"`
	Stage         OperationStage `json:"stage"`
	Scope         Scope          `json:"scope"`
}

// OperationLookupResult contains an optional prior committed outcome.
type OperationLookupResult struct {
	Found   bool             `json:"found"`
	Outcome OperationOutcome `json:"outcome,omitempty"`
}

// RevisionTransition atomically changes recall state for one prior revision.
type RevisionTransition struct {
	Ref  RevisionRef   `json:"ref"`
	From RevisionState `json:"from"`
	To   RevisionState `json:"to"`
}

// CommitRequest atomically applies one idempotent exact-scope operation.
type CommitRequest struct {
	SchemaVersion        string               `json:"schema_version"`
	OperationID          string               `json:"operation_id"`
	Stage                OperationStage       `json:"stage"`
	Scope                Scope                `json:"scope"`
	ExpectedScopeVersion uint64               `json:"expected_scope_version"`
	Sources              []SourceRecord       `json:"sources,omitempty"`
	Atoms                []Atom               `json:"atoms,omitempty"`
	Scenarios            []Scenario           `json:"scenarios,omitempty"`
	Profiles             []Profile            `json:"profiles,omitempty"`
	Transitions          []RevisionTransition `json:"transitions,omitempty"`
}

// Store is the atomic persistence port required by derived processing.
// Implementations must be concurrency-safe and enforce operation idempotency
// plus optimistic concurrency at the exact Scope boundary.
type Store interface {
	LookupOperation(ctx context.Context, lookup OperationLookup) (OperationLookupResult, error)
	LoadScope(ctx context.Context, scope Scope) (ScopeSnapshot, error)
	Commit(ctx context.Context, request CommitRequest) (OperationOutcome, error)
}

// AtomExtractionRequest is bounded input for atom extraction.
type AtomExtractionRequest struct {
	SchemaVersion string    `json:"schema_version"`
	Turn          Turn      `json:"turn"`
	View          ScopeView `json:"view"`
}

// ScenarioSynthesisRequest is bounded input for scenario synthesis.
type ScenarioSynthesisRequest struct {
	SchemaVersion string    `json:"schema_version"`
	Boundary      Boundary  `json:"boundary"`
	View          ScopeView `json:"view"`
}

// ProfileSynthesisRequest is bounded input for profile synthesis.
type ProfileSynthesisRequest struct {
	SchemaVersion string    `json:"schema_version"`
	Boundary      Boundary  `json:"boundary"`
	View          ScopeView `json:"view"`
}

// AtomExtractor converts one completed turn into untrusted atom candidates.
type AtomExtractor interface {
	ExtractAtoms(ctx context.Context, request AtomExtractionRequest) ([]AtomCandidate, error)
}

// ScenarioSynthesizer converts a boundary view into untrusted scenarios.
type ScenarioSynthesizer interface {
	SynthesizeScenarios(ctx context.Context, request ScenarioSynthesisRequest) ([]ScenarioCandidate, error)
}

// ProfileSynthesizer converts a boundary view into one untrusted profile.
type ProfileSynthesizer interface {
	SynthesizeProfile(ctx context.Context, request ProfileSynthesisRequest) (*ProfileCandidate, error)
}
