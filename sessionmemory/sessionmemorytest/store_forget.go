package sessionmemorytest

import (
	"context"
	"reflect"
	"sort"

	"github.com/normahq/balda/sessionmemory"
)

// ForgetSource implements sessionmemory.Store.
func (s *Store) ForgetSource(
	ctx context.Context,
	request sessionmemory.ForgetSourceRequest,
) (sessionmemory.ForgetOutcome, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	request, err := clone(request)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	state := s.stateLocked(request.Scope)
	if prior, ok := state.forgets[request.OperationID]; ok {
		if prior.sourceRequest == nil || !reflect.DeepEqual(*prior.sourceRequest, request) {
			return sessionmemory.ForgetOutcome{}, conflict("forget identity was reused with different content")
		}
		return clone(prior.outcome)
	}
	if request.ExpectedScopeVersion != state.snapshot.Version {
		return sessionmemory.ForgetOutcome{}, conflict("scope version changed")
	}
	if err := requireActiveSource(state.snapshot.Sources, request.Source); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	actual, err := dependentRevisions(state.snapshot, request.Source)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if !sameRevisionSet(actual, request.ExpectedRevisions) {
		return sessionmemory.ForgetOutcome{}, conflict("source forget cascade changed")
	}
	prospective, err := clone(state.snapshot)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	for index := range prospective.Sources {
		if prospective.Sources[index].Ref == request.Source {
			prospective.Sources[index].State = sessionmemory.SourceStateForgotten
			prospective.Sources[index].Turn = nil
			prospective.Sources[index].ForgottenAt = &request.ForgottenAt
		}
	}
	invalidateRevisions(&prospective, actual)
	prospective.Version++
	if err := prospective.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	sortRevisionRefs(actual)
	outcome := sessionmemory.ForgetOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          sessionmemory.ForgetKindSource,
		Scope:         request.Scope,
		ScopeVersion:  prospective.Version,
		Sources:       []sessionmemory.SourceRef{request.Source},
		Revisions:     actual,
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	state.snapshot = prospective
	requestCopy := request
	state.forgets[request.OperationID] = forgetRecord{sourceRequest: &requestCopy, outcome: outcome}
	return clone(outcome)
}

// ForgetScope implements sessionmemory.Store.
func (s *Store) ForgetScope(
	ctx context.Context,
	request sessionmemory.ForgetScopeRequest,
) (sessionmemory.ForgetOutcome, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	request, err := clone(request)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := contextError(ctx); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	state := s.stateLocked(request.Scope)
	if prior, ok := state.forgets[request.OperationID]; ok {
		if prior.scopeRequest == nil || !reflect.DeepEqual(*prior.scopeRequest, request) {
			return sessionmemory.ForgetOutcome{}, conflict("forget identity was reused with different content")
		}
		return clone(prior.outcome)
	}
	if request.ExpectedScopeVersion != state.snapshot.Version {
		return sessionmemory.ForgetOutcome{}, conflict("scope version changed")
	}
	actualSources := activeSources(state.snapshot.Sources)
	actualRevisions := readableRevisions(state.snapshot)
	if !sameSourceSet(actualSources, request.ExpectedSources) || !sameRevisionSet(actualRevisions, request.ExpectedRevisions) {
		return sessionmemory.ForgetOutcome{}, conflict("scope forget cascade changed")
	}
	prospective, err := clone(state.snapshot)
	if err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	activeSet := make(map[sessionmemory.SourceRef]struct{}, len(actualSources))
	for _, source := range actualSources {
		activeSet[source] = struct{}{}
	}
	for index := range prospective.Sources {
		if _, ok := activeSet[prospective.Sources[index].Ref]; ok {
			prospective.Sources[index].State = sessionmemory.SourceStateForgotten
			prospective.Sources[index].Turn = nil
			prospective.Sources[index].ForgottenAt = &request.ForgottenAt
		}
	}
	invalidateRevisions(&prospective, actualRevisions)
	prospective.Version++
	if err := prospective.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	sortSourceRefs(actualSources)
	sortRevisionRefs(actualRevisions)
	outcome := sessionmemory.ForgetOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Kind:          sessionmemory.ForgetKindScope,
		Scope:         request.Scope,
		ScopeVersion:  prospective.Version,
		Sources:       actualSources,
		Revisions:     actualRevisions,
	}
	if err := outcome.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	state.snapshot = prospective
	requestCopy := request
	state.forgets[request.OperationID] = forgetRecord{scopeRequest: &requestCopy, outcome: outcome}
	return clone(outcome)
}

func requireActiveSource(sources []sessionmemory.SourceRecord, want sessionmemory.SourceRef) error {
	for _, source := range sources {
		if source.Ref.ExportID != want.ExportID {
			continue
		}
		if source.Ref != want {
			return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "raw source does not exist", nil)
		}
		if source.State == sessionmemory.SourceStateForgotten {
			return sessionmemory.PermanentError(sessionmemory.CodeForgotten, "raw source was already forgotten", nil)
		}
		return nil
	}
	return sessionmemory.PermanentError(sessionmemory.CodeNotFound, "raw source does not exist", nil)
}

func dependentRevisions(
	snapshot sessionmemory.ScopeSnapshot,
	source sessionmemory.SourceRef,
) ([]sessionmemory.RevisionRef, error) {
	metas := snapshotMetas(snapshot)
	reverse := make(map[sessionmemory.RevisionRef][]sessionmemory.RevisionRef, len(metas))
	queue := make([]sessionmemory.RevisionRef, 0)
	for ref, meta := range metas {
		for _, raw := range meta.Provenance.RawSources {
			if raw == source {
				queue = append(queue, ref)
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if _, ok := metas[parent]; !ok {
				return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "missing provenance parent", nil)
			}
			reverse[parent] = append(reverse[parent], ref)
		}
	}
	seen := make(map[sessionmemory.RevisionRef]struct{}, len(queue))
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		queue = append(queue, reverse[ref]...)
	}
	refs := make([]sessionmemory.RevisionRef, 0, len(seen))
	for ref := range seen {
		state := metas[ref].State
		if state == sessionmemory.RevisionStateActive || state == sessionmemory.RevisionStateSuperseded {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func activeSources(sources []sessionmemory.SourceRecord) []sessionmemory.SourceRef {
	refs := make([]sessionmemory.SourceRef, 0, len(sources))
	for _, source := range sources {
		if source.State == sessionmemory.SourceStateActive {
			refs = append(refs, source.Ref)
		}
	}
	return refs
}

func readableRevisions(snapshot sessionmemory.ScopeSnapshot) []sessionmemory.RevisionRef {
	refs := make([]sessionmemory.RevisionRef, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for ref, meta := range snapshotMetas(snapshot) {
		if meta.State == sessionmemory.RevisionStateActive || meta.State == sessionmemory.RevisionStateSuperseded {
			refs = append(refs, ref)
		}
	}
	return refs
}

func invalidateRevisions(snapshot *sessionmemory.ScopeSnapshot, refs []sessionmemory.RevisionRef) {
	set := make(map[sessionmemory.RevisionRef]struct{}, len(refs))
	for _, ref := range refs {
		set[ref] = struct{}{}
	}
	for index := range snapshot.Atoms {
		if _, ok := set[revisionRef(snapshot.Atoms[index].Meta)]; ok {
			snapshot.Atoms[index].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
	for index := range snapshot.Scenarios {
		if _, ok := set[revisionRef(snapshot.Scenarios[index].Meta)]; ok {
			snapshot.Scenarios[index].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
	for index := range snapshot.Profiles {
		if _, ok := set[revisionRef(snapshot.Profiles[index].Meta)]; ok {
			snapshot.Profiles[index].Meta.State = sessionmemory.RevisionStateInvalidated
		}
	}
}

func sameSourceSet(left, right []sessionmemory.SourceRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[sessionmemory.SourceRef]struct{}, len(left))
	for _, ref := range left {
		set[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := set[ref]; !ok {
			return false
		}
	}
	return true
}

func sameRevisionSet(left, right []sessionmemory.RevisionRef) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[sessionmemory.RevisionRef]struct{}, len(left))
	for _, ref := range left {
		set[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := set[ref]; !ok {
			return false
		}
	}
	return true
}

func sortSourceRefs(refs []sessionmemory.SourceRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ExportID != refs[j].ExportID {
			return refs[i].ExportID < refs[j].ExportID
		}
		if refs[i].SessionID != refs[j].SessionID {
			return refs[i].SessionID < refs[j].SessionID
		}
		return refs[i].SourceTurnID < refs[j].SourceTurnID
	})
}

func sortRevisionRefs(refs []sessionmemory.RevisionRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ItemID != refs[j].ItemID {
			return refs[i].ItemID < refs[j].ItemID
		}
		return refs[i].RevisionID < refs[j].RevisionID
	})
}
