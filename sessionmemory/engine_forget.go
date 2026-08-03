package sessionmemory

import (
	"context"
	"math"
	"sort"
)

// ForgetSource atomically tombstones one raw source and invalidates every
// transitive derived dependent in the same exact scope. It never calls a model.
func (e *Engine) ForgetSource(ctx context.Context, command ForgetSourceCommand) (ForgetOutcome, error) {
	if e == nil {
		return ForgetOutcome{}, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return ForgetOutcome{}, err
	}
	if err := command.Validate(); err != nil {
		return ForgetOutcome{}, err
	}
	operationID := derivedStableID(
		"forget", string(ForgetKindSource), string(command.Source.Scope.Kind), command.Source.Scope.Key,
		command.Source.ExportID, command.Source.SessionID, command.Source.SourceTurnID,
	)
	lookup := ForgetLookup{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Kind:          ForgetKindSource,
		Scope:         command.Source.Scope,
	}
	prior, err := e.lookupForget(ctx, lookup)
	if err != nil {
		return ForgetOutcome{}, err
	}
	if prior.Found {
		if !sameSourceRefSet(prior.Outcome.Sources, []SourceRef{command.Source}) {
			return ForgetOutcome{}, invalidDerived("source forget replay does not match the requested source")
		}
		return canonicalForgetOutcome(prior.Outcome), nil
	}

	snapshot, err := e.loadForgetSnapshot(ctx, command.Source.Scope)
	if err != nil {
		return ForgetOutcome{}, err
	}
	if snapshot.Version == math.MaxUint64 {
		return ForgetOutcome{}, PermanentError(CodeConflict, "scope version cannot advance", nil)
	}
	source, err := activeSourceForForget(snapshot.Sources, command.Source)
	if err != nil {
		return ForgetOutcome{}, err
	}
	revisions, err := dependentRevisionClosure(snapshot, source)
	if err != nil {
		return ForgetOutcome{}, err
	}
	request := ForgetSourceRequest{
		SchemaVersion:        DerivedSchemaVersionV1,
		OperationID:          operationID,
		Scope:                command.Source.Scope,
		ExpectedScopeVersion: snapshot.Version,
		Source:               source,
		ExpectedRevisions:    revisions,
		ForgottenAt:          command.ForgottenAt,
	}
	if err := request.Validate(); err != nil {
		return ForgetOutcome{}, err
	}
	outcome, err := e.store.ForgetSource(ctx, cloneForgetSourceRequest(request))
	if err != nil {
		return ForgetOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return ForgetOutcome{}, err
	}
	if err := validateForgetOutcome(lookup, snapshot.Version, []SourceRef{source}, revisions, outcome); err != nil {
		return ForgetOutcome{}, err
	}
	return canonicalForgetOutcome(outcome), nil
}

// ForgetScope atomically tombstones every active raw source and invalidates
// every readable derived revision in one exact locator scope. It never calls a
// model and has no access to Balda's separate global fact memory.
func (e *Engine) ForgetScope(ctx context.Context, command ForgetScopeCommand) (ForgetOutcome, error) {
	if e == nil {
		return ForgetOutcome{}, invalidDerived("derived memory engine is required")
	}
	if err := checkContext(ctx); err != nil {
		return ForgetOutcome{}, err
	}
	if err := command.Validate(); err != nil {
		return ForgetOutcome{}, err
	}
	operationID := derivedStableID(
		"forget", string(ForgetKindScope), string(command.Scope.Kind), command.Scope.Key, command.RequestID,
	)
	lookup := ForgetLookup{
		SchemaVersion: DerivedSchemaVersionV1,
		OperationID:   operationID,
		Kind:          ForgetKindScope,
		Scope:         command.Scope,
	}
	prior, err := e.lookupForget(ctx, lookup)
	if err != nil {
		return ForgetOutcome{}, err
	}
	if prior.Found {
		return canonicalForgetOutcome(prior.Outcome), nil
	}

	snapshot, err := e.loadForgetSnapshot(ctx, command.Scope)
	if err != nil {
		return ForgetOutcome{}, err
	}
	if snapshot.Version == math.MaxUint64 {
		return ForgetOutcome{}, PermanentError(CodeConflict, "scope version cannot advance", nil)
	}
	sources := activeSourceRefs(snapshot.Sources)
	revisions := readableRevisionRefs(snapshot)
	request := ForgetScopeRequest{
		SchemaVersion:        DerivedSchemaVersionV1,
		OperationID:          operationID,
		Scope:                command.Scope,
		ExpectedScopeVersion: snapshot.Version,
		ExpectedSources:      sources,
		ExpectedRevisions:    revisions,
		ForgottenAt:          command.ForgottenAt,
	}
	if err := request.Validate(); err != nil {
		return ForgetOutcome{}, err
	}
	outcome, err := e.store.ForgetScope(ctx, cloneForgetScopeRequest(request))
	if err != nil {
		return ForgetOutcome{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return ForgetOutcome{}, err
	}
	if err := validateForgetOutcome(lookup, snapshot.Version, sources, revisions, outcome); err != nil {
		return ForgetOutcome{}, err
	}
	return canonicalForgetOutcome(outcome), nil
}

func (e *Engine) lookupForget(ctx context.Context, lookup ForgetLookup) (ForgetLookupResult, error) {
	prior, err := e.store.LookupForget(ctx, lookup)
	if err != nil {
		return ForgetLookupResult{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return ForgetLookupResult{}, err
	}
	prior = cloneForgetLookupResult(prior)
	if err := prior.Validate(lookup); err != nil {
		return ForgetLookupResult{}, err
	}
	return prior, nil
}

func (e *Engine) loadForgetSnapshot(ctx context.Context, scope Scope) (ScopeSnapshot, error) {
	snapshot, err := e.store.LoadScope(ctx, scope)
	if err != nil {
		return ScopeSnapshot{}, storePortFailure(ctx, err)
	}
	if err := checkContext(ctx); err != nil {
		return ScopeSnapshot{}, err
	}
	snapshot = cloneScopeSnapshot(snapshot)
	if err := snapshot.Validate(e.config.MaxSnapshotItems); err != nil {
		return ScopeSnapshot{}, err
	}
	if snapshot.Scope != scope {
		return ScopeSnapshot{}, PermanentError(CodeScopeViolation, "Store snapshot scope does not match the forget operation", nil)
	}
	return snapshot, nil
}

func activeSourceForForget(sources []SourceRecord, want SourceRef) (SourceRef, error) {
	for _, source := range sources {
		if source.Ref.ExportID != want.ExportID {
			continue
		}
		if source.Ref != want {
			return SourceRef{}, PermanentError(CodeNotFound, "raw source does not exist", nil)
		}
		if source.State == SourceStateForgotten {
			return SourceRef{}, PermanentError(CodeForgotten, "raw source was already forgotten", nil)
		}
		return source.Ref, nil
	}
	return SourceRef{}, PermanentError(CodeNotFound, "raw source does not exist", nil)
}

func activeSourceRefs(sources []SourceRecord) []SourceRef {
	refs := make([]SourceRef, 0, len(sources))
	for _, source := range sources {
		if source.State == SourceStateActive {
			refs = append(refs, source.Ref)
		}
	}
	sortSourceRefs(refs)
	return refs
}

func readableRevisionRefs(snapshot ScopeSnapshot) []RevisionRef {
	refs := make([]RevisionRef, 0, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	visit := func(meta RevisionMeta) {
		if meta.State == RevisionStateActive || meta.State == RevisionStateSuperseded {
			refs = append(refs, revisionRef(meta))
		}
	}
	for _, atom := range snapshot.Atoms {
		visit(atom.Meta)
	}
	for _, scenario := range snapshot.Scenarios {
		visit(scenario.Meta)
	}
	for _, profile := range snapshot.Profiles {
		visit(profile.Meta)
	}
	sortRevisionRefs(refs)
	return refs
}

func dependentRevisionClosure(snapshot ScopeSnapshot, source SourceRef) ([]RevisionRef, error) {
	metas := make(map[RevisionRef]RevisionMeta, len(snapshot.Atoms)+len(snapshot.Scenarios)+len(snapshot.Profiles))
	add := func(meta RevisionMeta) {
		metas[revisionRef(meta)] = meta
	}
	for _, atom := range snapshot.Atoms {
		add(atom.Meta)
	}
	for _, scenario := range snapshot.Scenarios {
		add(scenario.Meta)
	}
	for _, profile := range snapshot.Profiles {
		add(profile.Meta)
	}
	sourceSet := make(map[SourceRef]struct{}, len(snapshot.Sources))
	for _, record := range snapshot.Sources {
		sourceSet[record.Ref] = struct{}{}
	}
	reverse := make(map[RevisionRef][]RevisionRef, len(metas))
	queue := make([]RevisionRef, 0)
	for ref, meta := range metas {
		for _, raw := range meta.Provenance.RawSources {
			if _, ok := sourceSet[raw]; !ok {
				return nil, invalidDerived("derived provenance references a missing raw source")
			}
			if raw == source {
				queue = append(queue, ref)
			}
		}
		for _, parent := range meta.Provenance.ParentRevisions {
			if _, ok := metas[parent]; !ok {
				return nil, invalidDerived("derived provenance references a missing parent revision")
			}
			reverse[parent] = append(reverse[parent], ref)
		}
	}
	if err := validateAcyclicRevisionGraph(metas); err != nil {
		return nil, err
	}
	seen := make(map[RevisionRef]struct{}, len(queue))
	for len(queue) > 0 {
		ref := queue[0]
		queue = queue[1:]
		if _, ok := seen[ref]; ok {
			continue
		}
		seen[ref] = struct{}{}
		queue = append(queue, reverse[ref]...)
	}
	refs := make([]RevisionRef, 0, len(seen))
	for ref := range seen {
		state := metas[ref].State
		if state == RevisionStateActive || state == RevisionStateSuperseded {
			refs = append(refs, ref)
		}
	}
	sortRevisionRefs(refs)
	return refs, nil
}

func validateAcyclicRevisionGraph(metas map[RevisionRef]RevisionMeta) error {
	const (
		unvisited uint8 = iota
		visiting
		visited
	)
	states := make(map[RevisionRef]uint8, len(metas))
	var visit func(RevisionRef) error
	visit = func(ref RevisionRef) error {
		switch states[ref] {
		case visiting:
			return invalidDerived("derived provenance contains a cycle")
		case visited:
			return nil
		}
		states[ref] = visiting
		for _, parent := range metas[ref].Provenance.ParentRevisions {
			if err := visit(parent); err != nil {
				return err
			}
		}
		states[ref] = visited
		return nil
	}
	for ref := range metas {
		if states[ref] == unvisited {
			if err := visit(ref); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateForgetOutcome(
	lookup ForgetLookup,
	expectedVersion uint64,
	expectedSources []SourceRef,
	expectedRevisions []RevisionRef,
	outcome ForgetOutcome,
) error {
	if err := outcome.Validate(); err != nil {
		return err
	}
	if outcome.OperationID != lookup.OperationID || outcome.Kind != lookup.Kind || outcome.Scope != lookup.Scope {
		return PermanentError(CodeScopeViolation, "forget outcome does not match the request", nil)
	}
	if outcome.ScopeVersion != expectedVersion+1 {
		return PermanentError(CodeConflict, "forget outcome contains an unexpected scope version", nil)
	}
	if !sameSourceRefSet(outcome.Sources, expectedSources) || !sameRevisionRefSet(outcome.Revisions, expectedRevisions) {
		return invalidDerived("forget outcome does not match the requested cascade")
	}
	return nil
}

func sameSourceRefSet(left, right []SourceRef) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[SourceRef]struct{}, len(left))
	for _, ref := range left {
		seen[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := seen[ref]; !ok {
			return false
		}
	}
	return true
}

func sameRevisionRefSet(left, right []RevisionRef) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[RevisionRef]struct{}, len(left))
	for _, ref := range left {
		seen[ref] = struct{}{}
	}
	for _, ref := range right {
		if _, ok := seen[ref]; !ok {
			return false
		}
	}
	return true
}

func sortSourceRefs(refs []SourceRef) {
	sort.Slice(refs, func(i, j int) bool {
		left, right := refs[i], refs[j]
		if left.ExportID != right.ExportID {
			return left.ExportID < right.ExportID
		}
		if left.SessionID != right.SessionID {
			return left.SessionID < right.SessionID
		}
		return left.SourceTurnID < right.SourceTurnID
	})
}

func sortRevisionRefs(refs []RevisionRef) {
	sort.Slice(refs, func(i, j int) bool {
		if refs[i].ItemID != refs[j].ItemID {
			return refs[i].ItemID < refs[j].ItemID
		}
		return refs[i].RevisionID < refs[j].RevisionID
	})
}

func canonicalForgetOutcome(outcome ForgetOutcome) ForgetOutcome {
	outcome = cloneForgetOutcome(outcome)
	sortSourceRefs(outcome.Sources)
	sortRevisionRefs(outcome.Revisions)
	return outcome
}
