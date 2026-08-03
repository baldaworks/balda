package sessionmemorytest

import (
	"context"
	"math"
	"reflect"

	"github.com/normahq/balda/sessionmemory"
)

// Commit implements sessionmemory.Store.
func (s *Store) Commit(
	ctx context.Context,
	request sessionmemory.CommitRequest,
) (sessionmemory.OperationOutcome, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := request.Validate(); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	request, err := clone(request)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.stateLocked(request.Scope)
	if prior, ok := state.operations[request.OperationID]; ok {
		if !reflect.DeepEqual(prior.request, request) {
			return sessionmemory.OperationOutcome{}, conflict("operation identity was reused with different content")
		}
		return clone(prior.outcome)
	}
	if request.ExpectedScopeVersion != state.snapshot.Version {
		return sessionmemory.OperationOutcome{}, conflict("scope version changed")
	}
	prospective, err := clone(state.snapshot)
	if err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := validateCommitAgainstSnapshot(prospective, request); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	if err := applyCommit(&prospective, request); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	prospective.Version++
	if err := prospective.Validate(sessionmemory.MaxSnapshotItems); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	outcome := commitOutcome(request, prospective.Version)
	if err := outcome.Validate(); err != nil {
		return sessionmemory.OperationOutcome{}, err
	}
	state.snapshot = prospective
	state.operations[request.OperationID] = operationRecord{request: request, outcome: outcome}
	return clone(outcome)
}

func validateCommitAgainstSnapshot(
	snapshot sessionmemory.ScopeSnapshot,
	request sessionmemory.CommitRequest,
) error {
	if len(snapshot.Sources)+len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles)+
		len(request.Sources)+len(request.Atoms)+len(request.Scenarios)+len(request.Profiles) > sessionmemory.MaxSnapshotItems {
		return sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "test Store scope exceeds the item limit", nil)
	}
	existingSources := make(map[sessionmemory.SourceRef]sessionmemory.SourceState, len(snapshot.Sources)+len(request.Sources))
	exportIDs := make(map[string]struct{}, len(snapshot.Sources)+len(request.Sources))
	for _, source := range snapshot.Sources {
		existingSources[source.Ref] = source.State
		exportIDs[source.Ref.ExportID] = struct{}{}
	}
	for _, source := range request.Sources {
		if _, exists := exportIDs[source.Ref.ExportID]; exists {
			return conflict("raw source identity already exists")
		}
		exportIDs[source.Ref.ExportID] = struct{}{}
		existingSources[source.Ref] = source.State
	}

	existing := snapshotMetas(snapshot)
	created := requestMetas(request)
	for ref := range created {
		if _, exists := existing[ref]; exists {
			return conflict("derived revision identity already exists")
		}
	}
	createdItems := make(map[string]struct{}, len(created))
	for _, meta := range created {
		if _, exists := createdItems[meta.ItemID]; exists {
			return conflict("commit creates more than one revision of a logical item")
		}
		createdItems[meta.ItemID] = struct{}{}
	}
	all := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(existing)+len(created))
	for ref, meta := range existing {
		all[ref] = meta
	}
	for ref, meta := range created {
		all[ref] = meta
	}

	for _, transition := range request.Transitions {
		meta, ok := existing[transition.Ref]
		if !ok || meta.State != transition.From {
			return conflict("revision transition precondition changed")
		}
	}
	for ref, meta := range created {
		next, ok := nextRevision(existing, ref.ItemID)
		if !ok || meta.Revision != next {
			return conflict("derived revision number is not the next revision")
		}
		for _, source := range meta.Provenance.RawSources {
			if existingSources[source] != sessionmemory.SourceStateActive {
				return conflict("derived revision references an inactive raw source")
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			parentMeta, ok := all[parent]
			if !ok || parentMeta.State != sessionmemory.RevisionStateActive {
				return conflict("derived revision references an inactive parent")
			}
		}
	}
	return validateAcyclicMetas(all)
}

func applyCommit(snapshot *sessionmemory.ScopeSnapshot, request sessionmemory.CommitRequest) error {
	for _, transition := range request.Transitions {
		if !applyTransition(snapshot, transition) {
			return conflict("revision transition precondition changed")
		}
	}
	snapshot.Sources = append(snapshot.Sources, request.Sources...)
	snapshot.Atoms = append(snapshot.Atoms, request.Atoms...)
	snapshot.Scenarios = append(snapshot.Scenarios, request.Scenarios...)
	snapshot.Profiles = append(snapshot.Profiles, request.Profiles...)
	return nil
}

func applyTransition(snapshot *sessionmemory.ScopeSnapshot, transition sessionmemory.RevisionTransition) bool {
	for index := range snapshot.Atoms {
		if revisionRef(snapshot.Atoms[index].Meta) == transition.Ref && snapshot.Atoms[index].Meta.State == transition.From {
			snapshot.Atoms[index].Meta.State = transition.To
			return true
		}
	}
	for index := range snapshot.Scenarios {
		if revisionRef(snapshot.Scenarios[index].Meta) == transition.Ref && snapshot.Scenarios[index].Meta.State == transition.From {
			snapshot.Scenarios[index].Meta.State = transition.To
			return true
		}
	}
	for index := range snapshot.Profiles {
		if revisionRef(snapshot.Profiles[index].Meta) == transition.Ref && snapshot.Profiles[index].Meta.State == transition.From {
			snapshot.Profiles[index].Meta.State = transition.To
			return true
		}
	}
	return false
}

func snapshotMetas(snapshot sessionmemory.ScopeSnapshot) map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta {
	metas := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	for _, atom := range snapshot.Atoms {
		metas[revisionRef(atom.Meta)] = atom.Meta
	}
	for _, scenario := range snapshot.Scenarios {
		metas[revisionRef(scenario.Meta)] = scenario.Meta
	}
	for _, profile := range snapshot.Profiles {
		metas[revisionRef(profile.Meta)] = profile.Meta
	}
	return metas
}

func requestMetas(request sessionmemory.CommitRequest) map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta {
	metas := make(map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, len(request.Atoms)+len(request.Scenarios)+len(request.Profiles))
	for _, atom := range request.Atoms {
		metas[revisionRef(atom.Meta)] = atom.Meta
	}
	for _, scenario := range request.Scenarios {
		metas[revisionRef(scenario.Meta)] = scenario.Meta
	}
	for _, profile := range request.Profiles {
		metas[revisionRef(profile.Meta)] = profile.Meta
	}
	return metas
}

func nextRevision(existing map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta, itemID string) (uint64, bool) {
	var maximum uint64
	for _, meta := range existing {
		if meta.ItemID == itemID && meta.Revision > maximum {
			maximum = meta.Revision
		}
	}
	if maximum == math.MaxUint64 {
		return 0, false
	}
	return maximum + 1, true
}

func validateAcyclicMetas(metas map[sessionmemory.RevisionRef]sessionmemory.RevisionMeta) error {
	colors := make(map[sessionmemory.RevisionRef]uint8, len(metas))
	var visit func(sessionmemory.RevisionRef) error
	visit = func(ref sessionmemory.RevisionRef) error {
		switch colors[ref] {
		case 1:
			return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "provenance cycle", nil)
		case 2:
			return nil
		}
		colors[ref] = 1
		for _, parent := range metas[ref].Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		colors[ref] = 2
		return nil
	}
	for ref := range metas {
		if err := visit(ref); err != nil {
			return err
		}
	}
	return nil
}

func commitOutcome(request sessionmemory.CommitRequest, version uint64) sessionmemory.OperationOutcome {
	refs := make([]sessionmemory.RevisionRef, 0, len(request.Atoms)+len(request.Scenarios)+len(request.Profiles))
	for _, atom := range request.Atoms {
		refs = append(refs, revisionRef(atom.Meta))
	}
	for _, scenario := range request.Scenarios {
		refs = append(refs, revisionRef(scenario.Meta))
	}
	for _, profile := range request.Profiles {
		refs = append(refs, revisionRef(profile.Meta))
	}
	return sessionmemory.OperationOutcome{
		SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
		OperationID:   request.OperationID,
		Stage:         request.Stage,
		Scope:         request.Scope,
		ScopeVersion:  version,
		Revisions:     refs,
	}
}
