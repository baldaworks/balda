package sessionmemory

import (
	"context"
	"math"
	"sort"
	"strings"
	"time"
)

const (
	// RecallSchemaVersionV1 identifies the bounded canonical recall contract.
	RecallSchemaVersionV1 = "session-memory-recall/v1"
	// MaxRecallCandidates bounds projection candidates before canonical
	// hydration. The bound prevents a stale or broad index from turning one
	// request into a full-scope read.
	MaxRecallCandidates = 256
	// MaxRecallEvidence bounds compact evidence returned with one recall hit.
	MaxRecallEvidence = 16
)

// RecallRequest is the storage-neutral, exact-scope retrieval request. It is
// intentionally separate from other application capabilities so canonical
// retrieval can evolve without widening unrelated contracts.
type RecallRequest struct {
	SchemaVersion     string        `json:"schema_version"`
	Scope             Scope         `json:"scope"`
	Query             string        `json:"query"`
	Limit             int           `json:"limit"`
	Kind              *MemoryKind   `json:"kind,omitempty"`
	Category          *AtomCategory `json:"category,omitempty"`
	AsOf              *time.Time    `json:"as_of,omitempty"`
	SourceID          string        `json:"source_id,omitempty"`
	SessionID         string        `json:"session_id,omitempty"`
	MemoryKey         MemoryKey     `json:"memory_key,omitempty"`
	MinScopeChangeSeq uint64        `json:"min_scope_change_seq,omitempty"`
}

// RecallProjectionDocument is the rebuildable input to the lexical projection.
// Text is disposable projection material; canonical state remains authoritative
// and is always hydrated before a result is returned.
type RecallProjectionDocument struct {
	Scope          Scope
	ItemID         string
	RevisionID     string
	Revision       uint64
	Kind           MemoryKind
	Category       *AtomCategory
	MemoryKey      MemoryKey
	Text           string
	CreatedAt      time.Time
	Temporal       Temporal
	Sensitivity    Sensitivity
	Retention      RetentionClass
	SourceIDs      []string
	SessionIDs     []string
	ScopeChangeSeq uint64
}

// RecallProjectionHit is the bounded metadata returned by a projection. The
// projection never becomes the source of truth: RecallService hydrates the
// corresponding canonical records by RevisionID.
type RecallProjectionHit struct {
	Scope          Scope
	ItemID         string
	RevisionID     string
	Revision       uint64
	Score          float64
	ScopeChangeSeq uint64
}

// RecallRecord is one canonical record hydrated for recall. Implementations
// must not return a record from another scope or a non-active revision.
type RecallRecord struct {
	Scope             Scope
	ItemID            string
	RevisionID        string
	Revision          uint64
	Kind              MemoryKind
	Category          *AtomCategory
	LegacyKind        *DerivedKind
	LegacyItemID      string
	LegacyRevisionID  string
	LegacyOperationID string
	LegacySupersedes  *RevisionRef
	LegacyParents     []RevisionRef
	TopicKey          string
	Title             string
	MemoryKey         MemoryKey
	Text              string
	State             RevisionState
	CreatedAt         time.Time
	Temporal          Temporal
	Sensitivity       Sensitivity
	Retention         RetentionClass
	Evidence          []EvidenceRef
	SourceIDs         []string
	// SourceRefs retains the transport-neutral source identities needed by
	// compatibility adapters. SourceIDs remains the canonical evidence filter
	// surface; implementations may omit SourceRefs when only recall is needed.
	SourceRefs     []SourceRef
	SessionIDs     []string
	ScopeChangeSeq uint64
}

// RecallCanonicalReader is the bounded canonical hydration/fallback port.
// SearchCanonicalTail must page or cap its work; it must never load a full
// scope merely to satisfy one recall request.
type RecallCanonicalReader interface {
	LoadRecallRecords(ctx context.Context, scope Scope, revisionIDs []string) ([]RecallRecord, error)
	SearchRecallTail(ctx context.Context, request RecallRequest, limit uint32) ([]RecallRecord, error)
	CurrentScopeChangeSeq(ctx context.Context, scope Scope) (uint64, error)
}

// RecallProjection is implemented by the rebuildable lexical projection.
type RecallProjection interface {
	SearchRecall(ctx context.Context, request RecallRequest) ([]RecallProjectionHit, error)
}

// RecallScore explains the bounded ranking components exposed to callers.
type RecallScore struct {
	Lexical    float64 `json:"lexical"`
	Freshness  float64 `json:"freshness"`
	ExactScope float64 `json:"exact_scope"`
	Total      float64 `json:"total"`
}

// RecallReference is an explicitly untrusted, compact recall result.
type RecallReference struct {
	SchemaVersion  string         `json:"schema_version"`
	Trust          ReferenceTrust `json:"trust"`
	Scope          Scope          `json:"scope"`
	ItemID         string         `json:"item_id"`
	RevisionID     string         `json:"revision_id"`
	Revision       uint64         `json:"revision"`
	Kind           MemoryKind     `json:"kind"`
	State          RevisionState  `json:"state"`
	Category       *AtomCategory  `json:"category,omitempty"`
	MemoryKey      MemoryKey      `json:"memory_key,omitempty"`
	Text           string         `json:"text"`
	CreatedAt      time.Time      `json:"created_at"`
	Score          float64        `json:"score"`
	Explain        RecallScore    `json:"explain"`
	Evidence       []EvidenceRef  `json:"evidence,omitempty"`
	ScopeChangeSeq uint64         `json:"scope_change_seq"`
}

// RecallResponse contains only bounded, hydrated untrusted references.
type RecallResponse struct {
	SchemaVersion  string            `json:"schema_version"`
	Trust          ReferenceTrust    `json:"trust"`
	Scope          Scope             `json:"scope"`
	ScopeChangeSeq uint64            `json:"scope_change_seq"`
	Results        []RecallReference `json:"results"`
}

// Validate verifies a bounded untrusted recall response for one request.
func (r RecallResponse) Validate(request RecallRequest) error {
	if r.SchemaVersion != RecallSchemaVersionV1 || r.Trust != ReferenceTrustUntrusted || r.Scope != request.Scope {
		return PermanentError(CodeScopeViolation, "recall response scope or trust marker is invalid", nil)
	}
	if len(r.Results) > request.Limit {
		return limitExceeded("recall response exceeds the requested limit")
	}
	seen := make(map[string]struct{}, len(r.Results))
	for _, result := range r.Results {
		if result.SchemaVersion != RecallSchemaVersionV1 || result.Trust != ReferenceTrustUntrusted || result.Scope != request.Scope || result.State != RevisionStateActive || result.Text == "" {
			return invalidDerived("recall response contains an invalid reference")
		}
		if _, exists := seen[result.RevisionID]; exists {
			return invalidDerived("recall response contains duplicate revisions")
		}
		seen[result.RevisionID] = struct{}{}
	}
	return nil
}

// NormalizeRecallRequest applies defaults and validates an exact-scope query.
func NormalizeRecallRequest(request RecallRequest) (RecallRequest, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.SchemaVersion == "" {
		request.SchemaVersion = RecallSchemaVersionV1
	}
	if request.Limit == 0 {
		request.Limit = DefaultSearchLimit
	}
	if err := request.Validate(); err != nil {
		return RecallRequest{}, err
	}
	return request, nil
}

// Validate verifies a bounded exact-scope canonical recall query.
func (r RecallRequest) Validate() error {
	if r.SchemaVersion != RecallSchemaVersionV1 {
		return PermanentError(CodeInvalidQuery, "unsupported recall schema version", nil)
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if r.Query == "" || strings.TrimSpace(r.Query) != r.Query || len(r.Query) > MaxSearchQueryBytes {
		return PermanentError(CodeInvalidQuery, "recall query is invalid", nil)
	}
	if r.Limit < 1 || r.Limit > MaxSearchLimit {
		return PermanentError(CodeInvalidQuery, "recall limit is outside the allowed range", nil)
	}
	if r.Kind != nil {
		switch *r.Kind {
		case MemoryKindState, MemoryKindEvent:
		default:
			return PermanentError(CodeInvalidQuery, "recall kind is invalid", nil)
		}
	}
	if r.Category != nil {
		if err := r.Category.Validate(); err != nil {
			return PermanentError(CodeInvalidQuery, "recall category is invalid", nil)
		}
	}
	for name, value := range map[string]string{"source": r.SourceID, "session": r.SessionID, "memory key": string(r.MemoryKey)} {
		if value != "" && !isCanonicalID(value) {
			return PermanentError(CodeInvalidQuery, name+" filter is invalid", nil)
		}
	}
	if r.AsOf != nil && r.AsOf.IsZero() {
		return PermanentError(CodeInvalidQuery, "recall as_of is invalid", nil)
	}
	return nil
}

// Validate verifies a projection document before it is indexed.
func (d RecallProjectionDocument) Validate() error {
	if err := d.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(d.ItemID) || !isCanonicalID(d.RevisionID) || d.Revision == 0 || strings.TrimSpace(d.Text) == "" || len(d.Text) > MaxDerivedTextBytes || d.CreatedAt.IsZero() {
		return invalidDerived("recall projection document identity or text is invalid")
	}
	switch d.Kind {
	case MemoryKindState, MemoryKindEvent:
	default:
		return invalidDerived("recall projection document kind is invalid")
	}
	if d.Category != nil {
		if err := d.Category.Validate(); err != nil {
			return err
		}
	}
	if err := d.Temporal.Validate(); err != nil {
		return err
	}
	if err := validateSensitivityRetention(d.Sensitivity, d.Retention); err != nil {
		return err
	}
	for _, sourceID := range d.SourceIDs {
		if !isCanonicalID(sourceID) {
			return invalidDerived("recall projection source identity is invalid")
		}
	}
	for _, sessionID := range d.SessionIDs {
		if !isCanonicalID(sessionID) {
			return invalidDerived("recall projection session identity is invalid")
		}
	}
	if d.ScopeChangeSeq == 0 {
		return invalidDerived("recall projection change sequence is required")
	}
	return nil
}

// Validate verifies hydrated canonical recall data before it is exposed.
func (r RecallRecord) Validate(request RecallRequest, now time.Time) error {
	if r.Scope != request.Scope {
		return PermanentError(CodeScopeViolation, "recall record scope does not match request", nil)
	}
	if !isCanonicalID(r.ItemID) || !isCanonicalID(r.RevisionID) || r.Revision == 0 || r.State != RevisionStateActive {
		return PermanentError(CodeForgotten, "recall record is not active", nil)
	}
	if r.LegacyKind != nil {
		if err := r.LegacyKind.Validate(); err != nil {
			return err
		}
		if !isCanonicalID(r.LegacyItemID) || !isCanonicalID(r.LegacyRevisionID) || !isCanonicalID(r.LegacyOperationID) {
			return invalidDerived("recall compatibility identity is invalid")
		}
		if r.LegacySupersedes != nil {
			if err := r.LegacySupersedes.Validate(); err != nil {
				return err
			}
		}
		if len(r.LegacyParents) > MaxSourcesPerRevision {
			return limitExceeded("recall compatibility parent bound exceeded")
		}
		for _, parent := range r.LegacyParents {
			if err := parent.Validate(); err != nil {
				return err
			}
		}
	}
	switch r.Kind {
	case MemoryKindState, MemoryKindEvent:
	default:
		return invalidDerived("recall record kind is invalid")
	}
	if r.Category != nil {
		if err := r.Category.Validate(); err != nil {
			return err
		}
	}
	if request.Kind != nil && r.Kind != *request.Kind {
		return PermanentError(CodeNotFound, "recall record does not match kind filter", nil)
	}
	if request.Category != nil && (r.Category == nil || *r.Category != *request.Category) {
		return PermanentError(CodeNotFound, "recall record does not match category filter", nil)
	}
	if request.MemoryKey != "" && r.MemoryKey != request.MemoryKey {
		return PermanentError(CodeNotFound, "recall record does not match memory filter", nil)
	}
	if strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxDerivedTextBytes || r.CreatedAt.IsZero() {
		return invalidDerived("recall record text or timestamp is invalid")
	}
	if err := r.Temporal.Validate(); err != nil {
		return err
	}
	if r.Sensitivity != SensitivityStandard {
		return PermanentError(CodeForgotten, "recall record sensitivity is not allowed", nil)
	}
	if r.Retention != RetentionClassStandard && r.Retention != RetentionClassEphemeral {
		return PermanentError(CodeForgotten, "recall record retention is not allowed", nil)
	}
	if r.ScopeChangeSeq == 0 || r.ScopeChangeSeq < request.MinScopeChangeSeq {
		return PermanentError(CodeConflict, "recall record is behind the requested scope consistency", nil)
	}
	for _, sourceID := range r.SourceIDs {
		if !isCanonicalID(sourceID) {
			return invalidDerived("recall source identity is invalid")
		}
	}
	for _, source := range r.SourceRefs {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != r.Scope {
			return PermanentError(CodeScopeViolation, "recall source reference scope does not match", nil)
		}
	}
	for _, sessionID := range r.SessionIDs {
		if !isCanonicalID(sessionID) {
			return invalidDerived("recall session identity is invalid")
		}
	}
	if request.SourceID != "" && !containsString(r.SourceIDs, request.SourceID) {
		return PermanentError(CodeNotFound, "recall record does not match source filter", nil)
	}
	if request.SessionID != "" && !containsString(r.SessionIDs, request.SessionID) {
		return PermanentError(CodeNotFound, "recall record does not match session filter", nil)
	}
	if len(r.SourceIDs) == 0 || len(r.Evidence) == 0 {
		return invalidDerived("recall record provenance is required")
	}
	if len(r.Evidence) > MaxRecallEvidence {
		return limitExceeded("recall evidence exceeds the compact bound")
	}
	for _, evidence := range r.Evidence {
		if err := evidence.Validate(); err != nil {
			return err
		}
		if evidence.SourceID == "" || !containsString(r.SourceIDs, evidence.SourceID) {
			return PermanentError(CodeScopeViolation, "recall evidence is not grounded in the record", nil)
		}
	}
	if request.AsOf != nil {
		asOf := request.AsOf.UTC()
		if r.Temporal.ValidFrom != nil && asOf.Before(r.Temporal.ValidFrom.UTC()) {
			return PermanentError(CodeNotFound, "recall record is not valid at as_of", nil)
		}
		if r.Temporal.ValidUntil != nil && asOf.After(r.Temporal.ValidUntil.UTC()) {
			return PermanentError(CodeNotFound, "recall record expired at as_of", nil)
		}
		if r.Temporal.ExpiresAt != nil && !asOf.Before(r.Temporal.ExpiresAt.UTC()) {
			return PermanentError(CodeNotFound, "recall record expired at as_of", nil)
		}
	} else if r.Temporal.ExpiresAt != nil && now.After(r.Temporal.ExpiresAt.UTC()) {
		return PermanentError(CodeNotFound, "recall record has expired", nil)
	}
	return nil
}

// RankRecall combines deterministic projection score and freshness. The
// caller supplies now so tests and rebuilds can use a stable reference clock.
func RankRecall(score float64, createdAt, now time.Time) RecallScore {
	if math.IsNaN(score) || math.IsInf(score, 0) || score < 0 {
		score = 0
	}
	freshness := 0.0
	if !createdAt.IsZero() && !now.Before(createdAt) {
		age := now.Sub(createdAt).Hours()
		freshness = 1 / (1 + age/24)
	}
	return RecallScore{Lexical: score, Freshness: freshness, ExactScope: 1, Total: score + freshness + 1}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// SortRecallReferences applies the stable score/freshness/revision tie-breaks.
func SortRecallReferences(values []RecallReference) {
	sort.SliceStable(values, func(i, j int) bool {
		if values[i].Score != values[j].Score {
			return values[i].Score > values[j].Score
		}
		if !values[i].CreatedAt.Equal(values[j].CreatedAt) {
			return values[i].CreatedAt.After(values[j].CreatedAt)
		}
		return values[i].RevisionID < values[j].RevisionID
	})
}
