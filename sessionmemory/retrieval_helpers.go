package sessionmemory

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

func validateTraceGraph(request TraceRequest, graph TraceGraph) error {
	if graph.SchemaVersion != DerivedSchemaVersionV1 {
		return invalidDerived("unsupported trace graph schema version")
	}
	if graph.Scope != request.Scope || graph.Root != request.Root {
		return PermanentError(CodeScopeViolation, "trace graph does not match the request", nil)
	}
	if len(graph.Revisions)+len(graph.Sources) > request.MaxNodes {
		return limitExceeded("trace graph exceeds the node limit")
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
