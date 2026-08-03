package sessionmemorytest

import (
	"context"
	"sort"
	"strings"

	"github.com/normahq/balda/sessionmemory"
)

// Search implements sessionmemory.Store with deterministic case-insensitive
// substring matching. The reusable contract does not require this ranking
// strategy from other Store implementations.
func (s *Store) Search(
	ctx context.Context,
	request sessionmemory.DerivedSearchRequest,
) ([]sessionmemory.SearchHit, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	normalized, err := sessionmemory.NormalizeDerivedSearchRequest(request)
	if err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[normalized.Scope]
	if state == nil {
		return nil, nil
	}
	query := strings.ToLower(normalized.Query)
	hits := make([]sessionmemory.SearchHit, 0)
	for index := range state.snapshot.Atoms {
		atom := state.snapshot.Atoms[index]
		if atom.Meta.State != sessionmemory.RevisionStateActive ||
			(normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindAtom) ||
			(normalized.Category != nil && *normalized.Category != atom.Category) ||
			!strings.Contains(strings.ToLower(atom.Text), query) {
			continue
		}
		score := 1.0
		hits = append(hits, sessionmemory.SearchHit{Atom: &atom, Score: &score})
	}
	if normalized.Category == nil {
		for index := range state.snapshot.Scenarios {
			scenario := state.snapshot.Scenarios[index]
			if scenario.Meta.State != sessionmemory.RevisionStateActive ||
				(normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindScenario) ||
				!strings.Contains(strings.ToLower(scenario.Title+" "+scenario.Summary), query) {
				continue
			}
			score := 1.0
			hits = append(hits, sessionmemory.SearchHit{Scenario: &scenario, Score: &score})
		}
		for index := range state.snapshot.Profiles {
			profile := state.snapshot.Profiles[index]
			if profile.Meta.State != sessionmemory.RevisionStateActive ||
				(normalized.Kind != nil && *normalized.Kind != sessionmemory.DerivedKindProfile) ||
				!strings.Contains(strings.ToLower(profile.Summary), query) {
				continue
			}
			score := 1.0
			hits = append(hits, sessionmemory.SearchHit{Profile: &profile, Score: &score})
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		left := hitMeta(hits[i])
		right := hitMeta(hits[j])
		if !left.CreatedAt.Equal(right.CreatedAt) {
			return left.CreatedAt.After(right.CreatedAt)
		}
		return left.RevisionID < right.RevisionID
	})
	if len(hits) > normalized.Limit {
		hits = hits[:normalized.Limit]
	}
	return clone(hits)
}

// Trace implements sessionmemory.Store and returns the exact closed provenance
// graph for one non-invalidated revision.
func (s *Store) Trace(
	ctx context.Context,
	request sessionmemory.TraceRequest,
) (sessionmemory.TraceGraph, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	normalized, err := sessionmemory.NormalizeTraceRequest(request, sessionmemory.MaxTraceNodes)
	if err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[normalized.Scope]
	if state == nil {
		return sessionmemory.TraceGraph{}, sessionmemory.PermanentError(sessionmemory.CodeNotFound, "trace root does not exist", nil)
	}
	hits := snapshotHits(state.snapshot)
	byRevision := make(map[sessionmemory.RevisionRef]sessionmemory.SearchHit, len(hits))
	for _, hit := range hits {
		byRevision[revisionRef(hitMeta(hit))] = hit
	}
	sources := make(map[sessionmemory.SourceRef]sessionmemory.SourceRecord, len(state.snapshot.Sources))
	for _, source := range state.snapshot.Sources {
		sources[source.Ref] = source
	}
	visitedRevisions := make(map[sessionmemory.RevisionRef]sessionmemory.SearchHit)
	visitedSources := make(map[sessionmemory.SourceRef]sessionmemory.SourceRecord)
	var visit func(sessionmemory.RevisionRef) error
	visit = func(ref sessionmemory.RevisionRef) error {
		if _, ok := visitedRevisions[ref]; ok {
			return nil
		}
		hit, ok := byRevision[ref]
		if !ok {
			return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "trace revision does not exist", nil)
		}
		meta := hitMeta(hit)
		if meta.State == sessionmemory.RevisionStateInvalidated {
			return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "trace revision was invalidated", nil)
		}
		visitedRevisions[ref] = hit
		if len(visitedRevisions)+len(visitedSources) > normalized.MaxNodes {
			return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "trace exceeds the node limit", nil)
		}
		for _, sourceRef := range meta.Provenance.RawSources {
			source, ok := sources[sourceRef]
			if !ok {
				return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "trace raw source does not exist", nil)
			}
			if source.State == sessionmemory.SourceStateForgotten {
				return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "trace raw source was forgotten", nil)
			}
			visitedSources[sourceRef] = source
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(normalized.Root); err != nil {
		return sessionmemory.TraceGraph{}, err
	}
	graph := sessionmemory.TraceGraph{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		Scope:         normalized.Scope,
		Root:          normalized.Root,
		Revisions:     make([]sessionmemory.SearchHit, 0, len(visitedRevisions)),
		Sources:       make([]sessionmemory.SourceRecord, 0, len(visitedSources)),
	}
	for _, hit := range visitedRevisions {
		graph.Revisions = append(graph.Revisions, hit)
	}
	for _, source := range visitedSources {
		graph.Sources = append(graph.Sources, source)
	}
	sort.Slice(graph.Revisions, func(i, j int) bool {
		return hitMeta(graph.Revisions[i]).RevisionID < hitMeta(graph.Revisions[j]).RevisionID
	})
	sort.Slice(graph.Sources, func(i, j int) bool {
		return graph.Sources[i].Ref.ExportID < graph.Sources[j].Ref.ExportID
	})
	return clone(graph)
}

func snapshotHits(snapshot sessionmemory.ScopeSnapshot) []sessionmemory.SearchHit {
	hits := make([]sessionmemory.SearchHit, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for index := range snapshot.Atoms {
		atom := snapshot.Atoms[index]
		hits = append(hits, sessionmemory.SearchHit{Atom: &atom})
	}
	for index := range snapshot.Scenarios {
		scenario := snapshot.Scenarios[index]
		hits = append(hits, sessionmemory.SearchHit{Scenario: &scenario})
	}
	for index := range snapshot.Profiles {
		profile := snapshot.Profiles[index]
		hits = append(hits, sessionmemory.SearchHit{Profile: &profile})
	}
	return hits
}

func hitMeta(hit sessionmemory.SearchHit) sessionmemory.RevisionMeta {
	if hit.Atom != nil {
		return hit.Atom.Meta
	}
	if hit.Scenario != nil {
		return hit.Scenario.Meta
	}
	return hit.Profile.Meta
}
