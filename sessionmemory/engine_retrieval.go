package sessionmemory

import (
	"context"
	"encoding/json"
	"sort"
)

// Search returns bounded structured memory marked as untrusted reference data.
func (e *Engine) Search(ctx context.Context, request DerivedSearchRequest) (DerivedSearchResponse, error) {
	if e == nil {
		return DerivedSearchResponse{}, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return DerivedSearchResponse{}, err
	}
	normalized, err := NormalizeDerivedSearchRequest(cloneDerivedSearchRequest(request))
	if err != nil {
		return DerivedSearchResponse{}, err
	}
	if normalized.Limit > e.config.MaxSearchResults {
		return DerivedSearchResponse{}, limitExceeded("derived search limit exceeds the configured bound")
	}
	hits, err := e.store.Search(ctx, cloneDerivedSearchRequest(normalized))
	if err != nil {
		return DerivedSearchResponse{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return DerivedSearchResponse{}, err
	}
	if len(hits) > normalized.Limit {
		return DerivedSearchResponse{}, limitExceeded("Store search results exceed the requested limit")
	}
	hits = cloneSearchHits(hits)
	results := make([]DerivedReference, 0, len(hits))
	for _, hit := range hits {
		meta, err := hit.Validate()
		if err != nil {
			return DerivedSearchResponse{}, err
		}
		if meta.Scope != normalized.Scope {
			return DerivedSearchResponse{}, PermanentError(CodeScopeViolation, "Store search hit scope does not match the request", nil)
		}
		if meta.State != RevisionStateActive {
			return DerivedSearchResponse{}, invalidDerived("Store search hit is not active")
		}
		reference := derivedReferenceFromHit(hit, meta)
		if normalized.Kind != nil && reference.Kind != *normalized.Kind {
			return DerivedSearchResponse{}, invalidDerived("Store search hit does not match the requested kind")
		}
		if normalized.Category != nil && (reference.Category == nil || *reference.Category != *normalized.Category) {
			return DerivedSearchResponse{}, invalidDerived("Store search hit does not match the requested category")
		}
		if err := reference.Validate(); err != nil {
			return DerivedSearchResponse{}, err
		}
		results = append(results, reference)
	}
	sortDerivedReferences(results)
	response := DerivedSearchResponse{
		SchemaVersion: DerivedSchemaVersionV1,
		Trust:         ReferenceTrustUntrusted,
		Scope:         normalized.Scope,
		Results:       results,
	}
	if err := response.Validate(normalized.Limit); err != nil {
		return DerivedSearchResponse{}, err
	}
	if err := validateRetrievalSize(response, e.config.MaxSearchResponseBytes); err != nil {
		return DerivedSearchResponse{}, err
	}
	return response, nil
}

// Trace returns one validated closed provenance graph marked as untrusted data.
func (e *Engine) Trace(ctx context.Context, request TraceRequest) (TraceResponse, error) {
	if e == nil {
		return TraceResponse{}, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return TraceResponse{}, err
	}
	normalized, err := NormalizeTraceRequest(request, e.config.MaxTraceNodes)
	if err != nil {
		return TraceResponse{}, err
	}
	if normalized.MaxNodes > e.config.MaxTraceNodes {
		return TraceResponse{}, limitExceeded("trace node limit exceeds the configured bound")
	}
	graph, err := e.store.Trace(ctx, normalized)
	if err != nil {
		return TraceResponse{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return TraceResponse{}, err
	}
	graph = cloneTraceGraph(graph)
	if err := validateTraceGraph(normalized, graph); err != nil {
		return TraceResponse{}, err
	}
	response := TraceResponse{
		SchemaVersion: DerivedSchemaVersionV1,
		Trust:         ReferenceTrustUntrusted,
		Scope:         graph.Scope,
		Root:          graph.Root,
		Revisions:     graph.Revisions,
		Sources:       graph.Sources,
	}
	if err := response.Validate(normalized.MaxNodes); err != nil {
		return TraceResponse{}, err
	}
	if err := validateRetrievalSize(response, e.config.MaxSearchResponseBytes); err != nil {
		return TraceResponse{}, err
	}
	return response, nil
}

func validateTraceGraph(request TraceRequest, graph TraceGraph) error {
	if graph.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported Store trace graph schema version")
	}
	if graph.Scope != request.Scope || graph.Root != request.Root {
		return PermanentError(CodeScopeViolation, "Store trace graph does not match the request", nil)
	}
	if len(graph.Revisions)+len(graph.Sources) > request.MaxNodes {
		return limitExceeded("Store trace graph exceeds the node limit")
	}
	revisions := make(map[RevisionRef]SearchHit, len(graph.Revisions))
	for _, hit := range graph.Revisions {
		if hit.Score != nil {
			return invalidDerived("trace revision cannot contain a search score")
		}
		meta, err := hit.Validate()
		if err != nil {
			return err
		}
		if meta.Scope != request.Scope {
			return PermanentError(CodeScopeViolation, "trace revision scope does not match the request", nil)
		}
		if meta.State == RevisionStateInvalidated {
			return PermanentError(CodeForgotten, "trace contains an invalidated revision", nil)
		}
		ref := revisionRef(meta)
		if _, exists := revisions[ref]; exists {
			return invalidDerived("trace contains a duplicate revision")
		}
		revisions[ref] = hit
	}
	sources := make(map[SourceRef]SourceRecord, len(graph.Sources))
	for _, source := range graph.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Ref.Scope != request.Scope {
			return PermanentError(CodeScopeViolation, "trace source scope does not match the request", nil)
		}
		if _, exists := sources[source.Ref]; exists {
			return invalidDerived("trace contains a duplicate raw source")
		}
		sources[source.Ref] = source
	}

	colors := make(map[RevisionRef]uint8, len(revisions))
	visitedSources := make(map[SourceRef]struct{}, len(sources))
	var visit func(RevisionRef) error
	visit = func(ref RevisionRef) error {
		switch colors[ref] {
		case 1:
			return invalidDerived("trace provenance contains a cycle")
		case 2:
			return nil
		}
		hit, ok := revisions[ref]
		if !ok {
			return PermanentError(CodeNotFound, "trace provenance revision is missing", nil)
		}
		colors[ref] = 1
		meta, err := hit.Validate()
		if err != nil {
			return err
		}
		for _, sourceRef := range meta.Provenance.RawSources {
			source, ok := sources[sourceRef]
			if !ok {
				return PermanentError(CodeNotFound, "trace provenance raw source is missing", nil)
			}
			if source.State == SourceStateForgotten {
				return PermanentError(CodeForgotten, "trace provenance source was forgotten", nil)
			}
			visitedSources[sourceRef] = struct{}{}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		colors[ref] = 2
		return nil
	}
	if err := visit(request.Root); err != nil {
		return err
	}
	visitedRevisions := 0
	for _, color := range colors {
		if color == 2 {
			visitedRevisions++
		}
	}
	if visitedRevisions != len(revisions) || len(visitedSources) != len(sources) {
		return invalidDerived("trace graph contains nodes outside the requested provenance closure")
	}
	return nil
}

func derivedReferenceFromHit(hit SearchHit, meta RevisionMeta) DerivedReference {
	reference := DerivedReference{
		SchemaVersion: DerivedSchemaVersionV1,
		Trust:         ReferenceTrustUntrusted,
		Kind:          meta.Kind,
		Scope:         meta.Scope,
		ItemID:        meta.ItemID,
		RevisionID:    meta.RevisionID,
		Revision:      meta.Revision,
		State:         meta.State,
		CreatedAt:     meta.CreatedAt,
		Score:         cloneScore(hit.Score),
		Provenance:    cloneRevisionMeta(meta).Provenance,
	}
	switch {
	case hit.Atom != nil:
		category := hit.Atom.Category
		reference.Category = &category
		reference.Text = hit.Atom.Text
	case hit.Scenario != nil:
		reference.TopicKey = hit.Scenario.TopicKey
		reference.Title = hit.Scenario.Title
		reference.Text = hit.Scenario.Summary
	case hit.Profile != nil:
		reference.Text = hit.Profile.Summary
	}
	return reference
}

func sortDerivedReferences(results []DerivedReference) {
	sort.Slice(results, func(left, right int) bool {
		return derivedReferenceLess(results[left], results[right])
	})
}

func derivedReferenceLess(left, right DerivedReference) bool {
	switch {
	case left.Score != nil && right.Score == nil:
		return true
	case left.Score == nil && right.Score != nil:
		return false
	case left.Score != nil && right.Score != nil && *left.Score != *right.Score:
		return *left.Score > *right.Score
	case !left.CreatedAt.Equal(right.CreatedAt):
		return left.CreatedAt.After(right.CreatedAt)
	default:
		return left.RevisionID < right.RevisionID
	}
}

func cloneDerivedSearchRequest(request DerivedSearchRequest) DerivedSearchRequest {
	if request.Kind != nil {
		kind := *request.Kind
		request.Kind = &kind
	}
	if request.Category != nil {
		category := *request.Category
		request.Category = &category
	}
	return request
}

func cloneScore(score *float64) *float64 {
	if score == nil {
		return nil
	}
	cloned := *score
	return &cloned
}

func validateRetrievalSize(value any, limit int) error {
	payload, err := json.Marshal(value)
	if err != nil {
		return invalidDerived("retrieval response cannot be encoded")
	}
	if len(payload) > limit {
		return limitExceeded("retrieval response exceeds the configured size limit")
	}
	return nil
}
