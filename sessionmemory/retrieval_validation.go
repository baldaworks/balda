package sessionmemory

import (
	"math"
	"strings"
)

// NormalizeDerivedSearchRequest applies defaults and validates one exact-scope search.
func NormalizeDerivedSearchRequest(request DerivedSearchRequest) (DerivedSearchRequest, error) {
	request.Query = strings.TrimSpace(request.Query)
	if request.SchemaVersion == "" {
		request.SchemaVersion = DerivedSchemaVersionV1
	}
	if request.Limit == 0 {
		request.Limit = DefaultSearchLimit
	}
	if err := request.Validate(); err != nil {
		return DerivedSearchRequest{}, err
	}
	return request, nil
}

// Validate verifies one bounded exact-scope derived search request.
func (r DerivedSearchRequest) Validate() error {
	if r.SchemaVersion != DerivedSchemaVersionV1 {
		return PermanentError(CodeInvalidQuery, "unsupported derived search schema version", nil)
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(r.Query) == "" || strings.TrimSpace(r.Query) != r.Query {
		return PermanentError(CodeInvalidQuery, "derived search query must be normalized", nil)
	}
	if len(r.Query) > MaxSearchQueryBytes {
		return PermanentError(CodeInvalidQuery, "derived search query exceeds the size limit", nil)
	}
	if r.Limit < 1 || r.Limit > MaxSearchLimit {
		return PermanentError(CodeInvalidQuery, "derived search limit is outside the allowed range", nil)
	}
	if r.Kind != nil {
		if err := r.Kind.Validate(); err != nil {
			return PermanentError(CodeInvalidQuery, "derived search kind is invalid", nil)
		}
	}
	if r.Category != nil {
		if err := r.Category.Validate(); err != nil {
			return PermanentError(CodeInvalidQuery, "derived search category is invalid", nil)
		}
		if r.Kind != nil && *r.Kind != DerivedKindAtom {
			return PermanentError(CodeInvalidQuery, "atom category filter requires atom kind", nil)
		}
	}
	for name, value := range map[string]string{"source": r.SourceID, "session": r.SessionID, "memory key": r.MemoryKey} {
		if value != "" && !isCanonicalID(value) {
			return PermanentError(CodeInvalidQuery, name+" filter is invalid", nil)
		}
	}
	if r.AsOf != nil && r.AsOf.IsZero() {
		return PermanentError(CodeInvalidQuery, "derived search as_of is invalid", nil)
	}
	return nil
}

// Validate verifies one Store search hit and returns its full revision metadata.
func (h SearchHit) Validate() (RevisionMeta, error) {
	populated := 0
	var meta RevisionMeta
	if h.Atom != nil {
		populated++
		if err := h.Atom.Validate(); err != nil {
			return RevisionMeta{}, err
		}
		meta = h.Atom.Meta
	}
	if h.Scenario != nil {
		populated++
		if err := h.Scenario.Validate(); err != nil {
			return RevisionMeta{}, err
		}
		meta = h.Scenario.Meta
	}
	if h.Profile != nil {
		populated++
		if err := h.Profile.Validate(); err != nil {
			return RevisionMeta{}, err
		}
		meta = h.Profile.Meta
	}
	if populated != 1 {
		return RevisionMeta{}, invalidDerived("search hit must contain exactly one derived revision")
	}
	if h.Score != nil && (math.IsNaN(*h.Score) || math.IsInf(*h.Score, 0)) {
		return RevisionMeta{}, invalidDerived("search hit score must be finite")
	}
	return meta, nil
}

// Validate verifies a structured untrusted recall reference.
func (r DerivedReference) Validate() error {
	if r.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported derived reference schema version")
	}
	if r.Trust != ReferenceTrustUntrusted {
		return invalidDerived("derived reference must be marked untrusted")
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if err := (RevisionRef{ItemID: r.ItemID, RevisionID: r.RevisionID}).Validate(); err != nil {
		return err
	}
	if r.Revision == 0 || r.State != RevisionStateActive || r.CreatedAt.IsZero() {
		return invalidDerived("derived reference requires an active revision identity")
	}
	if strings.TrimSpace(r.Text) == "" || len(r.Text) > MaxDerivedTextBytes {
		return invalidDerived("derived reference text is invalid")
	}
	if err := r.Provenance.Validate(r.Scope); err != nil {
		return err
	}
	if r.Score != nil && (math.IsNaN(*r.Score) || math.IsInf(*r.Score, 0)) {
		return invalidDerived("derived reference score must be finite")
	}
	switch r.Kind {
	case DerivedKindAtom:
		if r.Category == nil {
			return invalidDerived("atom reference requires a category")
		}
		if err := r.Category.Validate(); err != nil {
			return err
		}
		if r.TopicKey != "" || r.Title != "" {
			return invalidDerived("atom reference contains scenario fields")
		}
	case DerivedKindScenario:
		if r.Category != nil || strings.TrimSpace(r.TopicKey) == "" || strings.TrimSpace(r.Title) == "" {
			return invalidDerived("scenario reference fields are invalid")
		}
	case DerivedKindProfile:
		if r.Category != nil || r.TopicKey != "" || r.Title != "" {
			return invalidDerived("profile reference contains unrelated fields")
		}
	}
	return nil
}

// Validate verifies a bounded untrusted derived search response.
func (r DerivedSearchResponse) Validate(limit int) error {
	if r.SchemaVersion != DerivedSchemaVersionV1 || r.Trust != ReferenceTrustUntrusted {
		return invalidDerived("derived search response schema or trust marker is invalid")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if limit < 1 || len(r.Results) > limit {
		return limitExceeded("derived search response exceeds the result limit")
	}
	seen := make(map[RevisionRef]struct{}, len(r.Results))
	for _, result := range r.Results {
		if err := result.Validate(); err != nil {
			return err
		}
		if result.Scope != r.Scope {
			return PermanentError(CodeScopeViolation, "derived search result scope does not match the response", nil)
		}
		ref := RevisionRef{ItemID: result.ItemID, RevisionID: result.RevisionID}
		if _, ok := seen[ref]; ok {
			return invalidDerived("derived search response contains a duplicate revision")
		}
		seen[ref] = struct{}{}
	}
	for index := 1; index < len(r.Results); index++ {
		if derivedReferenceLess(r.Results[index], r.Results[index-1]) {
			return invalidDerived("derived search response ordering is not deterministic")
		}
	}
	return nil
}

// NormalizeTraceRequest applies the configured graph bound and validates the request.
func NormalizeTraceRequest(request TraceRequest, defaultMaxNodes int) (TraceRequest, error) {
	if request.SchemaVersion == "" {
		request.SchemaVersion = DerivedSchemaVersionV1
	}
	if request.MaxNodes == 0 {
		request.MaxNodes = defaultMaxNodes
	}
	if err := request.Validate(); err != nil {
		return TraceRequest{}, err
	}
	return request, nil
}

// Validate verifies one exact-scope bounded trace request.
func (r TraceRequest) Validate() error {
	if r.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported trace request schema version")
	}
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if err := r.Root.Validate(); err != nil {
		return err
	}
	if r.MaxNodes < 1 || r.MaxNodes > MaxTraceNodes {
		return limitExceeded("trace node limit is outside the allowed range")
	}
	return nil
}

// Validate verifies a closed, bounded trace response and its untrusted marker.
func (r TraceResponse) Validate(maxNodes int) error {
	if r.SchemaVersion != DerivedSchemaVersionV1 || r.Trust != ReferenceTrustUntrusted {
		return invalidDerived("trace response schema or trust marker is invalid")
	}
	request := TraceRequest{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         r.Scope,
		Root:          r.Root,
		MaxNodes:      maxNodes,
	}
	if err := request.Validate(); err != nil {
		return err
	}
	return validateTraceGraph(request, TraceGraph{
		SchemaVersion: DerivedSchemaVersionV1,
		Scope:         r.Scope,
		Root:          r.Root,
		Revisions:     r.Revisions,
		Sources:       r.Sources,
	})
}
