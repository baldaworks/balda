package sessionmemory

import "time"

// ForgetKind identifies one independently idempotent forgetting operation.
type ForgetKind string

const (
	// ForgetKindSource forgets one immutable raw turn and its dependents.
	ForgetKindSource ForgetKind = "source"
	// ForgetKindScope forgets all readable memory in one exact locator scope.
	ForgetKindScope ForgetKind = "scope"
)

// ForgetSourceCommand requests source-aware forgetting through Engine.
type ForgetSourceCommand struct {
	SchemaVersion string    `json:"schema_version"`
	Source        SourceRef `json:"source"`
	ForgottenAt   time.Time `json:"forgotten_at"`
}

// ForgetScopeCommand requests forgetting of one exact locator scope.
// RequestID is a caller-owned idempotency key, allowing a later request to
// forget content added to the same scope after an earlier operation.
type ForgetScopeCommand struct {
	SchemaVersion string    `json:"schema_version"`
	Scope         Scope     `json:"scope"`
	RequestID     string    `json:"request_id"`
	ForgottenAt   time.Time `json:"forgotten_at"`
}

// ForgetLookup asks whether one exact-scope forgetting operation committed.
type ForgetLookup struct {
	SchemaVersion string     `json:"schema_version"`
	OperationID   string     `json:"operation_id"`
	Kind          ForgetKind `json:"kind"`
	Scope         Scope      `json:"scope"`
}

// ForgetLookupResult contains an optional prior committed outcome.
type ForgetLookupResult struct {
	Found   bool          `json:"found"`
	Outcome ForgetOutcome `json:"outcome,omitempty"`
}

// ForgetSourceRequest is the atomic Store contract for one source cascade.
// ExpectedRevisions is the complete active-or-superseded reverse-provenance
// closure. The Store must reject a mismatch instead of partially forgetting.
type ForgetSourceRequest struct {
	SchemaVersion        string        `json:"schema_version"`
	OperationID          string        `json:"operation_id"`
	Scope                Scope         `json:"scope"`
	ExpectedScopeVersion uint64        `json:"expected_scope_version"`
	Source               SourceRef     `json:"source"`
	ExpectedRevisions    []RevisionRef `json:"expected_revisions,omitempty"`
	ForgottenAt          time.Time     `json:"forgotten_at"`
}

// ForgetScopeRequest is the atomic Store contract for an exact-scope cascade.
// ExpectedSources and ExpectedRevisions contain every currently readable raw
// source and every active-or-superseded derived revision in that scope.
type ForgetScopeRequest struct {
	SchemaVersion        string        `json:"schema_version"`
	OperationID          string        `json:"operation_id"`
	Scope                Scope         `json:"scope"`
	ExpectedScopeVersion uint64        `json:"expected_scope_version"`
	ExpectedSources      []SourceRef   `json:"expected_sources,omitempty"`
	ExpectedRevisions    []RevisionRef `json:"expected_revisions,omitempty"`
	ForgottenAt          time.Time     `json:"forgotten_at"`
}

// ForgetOutcome is the durable, content-free result of one atomic forget.
type ForgetOutcome struct {
	SchemaVersion string        `json:"schema_version"`
	OperationID   string        `json:"operation_id"`
	Kind          ForgetKind    `json:"kind"`
	Scope         Scope         `json:"scope"`
	ScopeVersion  uint64        `json:"scope_version"`
	Sources       []SourceRef   `json:"sources,omitempty"`
	Revisions     []RevisionRef `json:"revisions,omitempty"`
}
