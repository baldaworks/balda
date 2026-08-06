package state

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

// LoadActiveRecallRecords returns a bounded active-head page suitable for the
// boundary compatibility adapter. It deliberately has no query parameter and
// never walks historical revisions.
func (r *CanonicalReader) LoadActiveRecallRecords(ctx context.Context, scope sessionmemory.Scope, limit uint32) ([]sessionmemory.RecallRecord, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return nil, err
	}
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if limit == 0 || limit > sessionmemory.MaxRecallCandidates {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "active canonical recall limit is invalid", nil)
	}
	active, err := r.store.ScanActiveMemory(ctx, sessionmemory.ActiveMemoryScanRequest{Scope: scope, Limit: limit})
	if err != nil {
		return nil, err
	}
	if len(active) == int(limit) {
		probe, probeErr := r.store.ScanActiveMemory(ctx, sessionmemory.ActiveMemoryScanRequest{Scope: scope, AfterItemID: active[len(active)-1].Item.ItemID, Limit: 1})
		if probeErr != nil {
			return nil, probeErr
		}
		if len(probe) != 0 {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "canonical boundary view exceeds the bounded active limit", nil)
		}
	}
	result := make([]sessionmemory.RecallRecord, 0, len(active))
	for _, memory := range active {
		record, found, loadErr := r.loadRecallRecord(ctx, scope, memory.RevisionID, true)
		if loadErr != nil {
			return nil, loadErr
		}
		if found {
			result = append(result, record)
		}
	}
	return result, nil
}

var _ sessionmemory.CanonicalBoundaryViewReader = (*CanonicalReader)(nil)

// LoadCanonicalCompatibility resolves one exact canonical revision's
// rebuildable legacy presentation metadata, including superseded revisions.
// It is used only for idempotent boundary-operation replay; active reads stay
// on LoadActiveRecallRecords and never widen their bounded view.
func (r *CanonicalReader) LoadCanonicalCompatibility(ctx context.Context, scope sessionmemory.Scope, revisionID string) (sessionmemory.CanonicalCompatibilityPayload, bool, error) {
	if err := canonicalReaderContext(ctx); err != nil {
		return sessionmemory.CanonicalCompatibilityPayload{}, false, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.CanonicalCompatibilityPayload{}, false, err
	}
	view, found, err := r.loadCanonicalView(ctx, scope, revisionID, false)
	if err != nil || !found {
		return sessionmemory.CanonicalCompatibilityPayload{}, found, err
	}
	payload, err := r.store.LoadPayload(ctx, view.revision.Payload)
	if err != nil {
		return sessionmemory.CanonicalCompatibilityPayload{}, false, err
	}
	compatibility, ok := canonicalCompatibilityPayload(payload, scope)
	return compatibility, ok, nil
}

// LoadCanonicalBoundaryView exposes only a validated bounded compatibility
// view. Canonical v2 IDs remain in LegacyToCanonical for write translation.
func (r *CanonicalReader) LoadCanonicalBoundaryView(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.CanonicalBoundaryView, error) {
	records, err := r.LoadActiveRecallRecords(ctx, scope, sessionmemory.MaxRecallCandidates)
	if err != nil {
		return sessionmemory.CanonicalBoundaryView{}, err
	}
	state, err := r.store.LoadScopeState(ctx, scope)
	if err != nil {
		return sessionmemory.CanonicalBoundaryView{}, err
	}
	view := sessionmemory.CanonicalBoundaryView{Snapshot: sessionmemory.ScopeSnapshot{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Scope: scope, Version: state.Version}, LegacyToCanonical: make(map[sessionmemory.RevisionRef]sessionmemory.RevisionRef)}
	for _, record := range records {
		provenance := sessionmemory.Provenance{ParentRevisions: append([]sessionmemory.RevisionRef(nil), record.LegacyParents...)}
		if record.LegacyKind == nil || *record.LegacyKind == sessionmemory.DerivedKindAtom {
			provenance.RawSources = append(provenance.RawSources, record.SourceRefs...)
		}
		if err := provenance.Validate(scope); err != nil {
			return sessionmemory.CanonicalBoundaryView{}, err
		}
		kind := sessionmemory.DerivedKindAtom
		if record.LegacyKind != nil {
			kind = *record.LegacyKind
		}
		operationStage := sessionmemory.OperationStageAtoms
		if kind == sessionmemory.DerivedKindScenario {
			operationStage = sessionmemory.OperationStageScenarios
		}
		if kind == sessionmemory.DerivedKindProfile {
			operationStage = sessionmemory.OperationStageProfile
		}
		operationID := record.LegacyOperationID
		if operationID == "" {
			var opErr error
			operationID, opErr = sessionmemory.ProcessingOperationID(operationStage, record.RevisionID, sessionmemory.LegacyDerivationRef())
			if opErr != nil {
				return sessionmemory.CanonicalBoundaryView{}, opErr
			}
		}
		legacyRef := sessionmemory.RevisionRef{}
		switch kind {
		case sessionmemory.DerivedKindAtom:
			category := sessionmemory.AtomCategoryFact
			if record.Category != nil {
				category = *record.Category
			}
			itemID := record.LegacyItemID
			if itemID == "" {
				var idErr error
				itemID, idErr = sessionmemory.AtomItemID(scope, category, record.Text)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			meta := sessionmemory.RevisionMeta{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Kind: kind, ItemID: itemID, Revision: record.Revision, OperationID: operationID, Scope: scope, State: sessionmemory.RevisionStateActive, Provenance: provenance, CreatedAt: record.CreatedAt}
			revisionID := record.LegacyRevisionID
			if revisionID == "" {
				var idErr error
				revisionID, idErr = sessionmemory.DerivedRevisionID(scope, itemID, operationID, []string{string(category), record.Text, string(sessionmemory.CandidateRelationNew)}, provenance, nil)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			meta.RevisionID = revisionID
			atom := sessionmemory.Atom{Meta: meta, Category: category, Text: record.Text, Relation: sessionmemory.CandidateRelationNew}
			if err := atom.Validate(); err != nil {
				return sessionmemory.CanonicalBoundaryView{}, err
			}
			view.Snapshot.Atoms = append(view.Snapshot.Atoms, atom)
			legacyRef = sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
		case sessionmemory.DerivedKindScenario:
			itemID := record.LegacyItemID
			if itemID == "" {
				var idErr error
				itemID, idErr = sessionmemory.ScenarioItemID(scope, record.TopicKey)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			meta := sessionmemory.RevisionMeta{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Kind: kind, ItemID: itemID, Revision: record.Revision, OperationID: operationID, Scope: scope, State: sessionmemory.RevisionStateActive, Provenance: provenance, CreatedAt: record.CreatedAt}
			revisionID := record.LegacyRevisionID
			if revisionID == "" {
				var idErr error
				revisionID, idErr = sessionmemory.DerivedRevisionID(scope, itemID, operationID, []string{record.TopicKey, record.Title, record.Text}, provenance, nil)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			if record.LegacySupersedes != nil {
				copyOf := *record.LegacySupersedes
				meta.Supersedes = &copyOf
			}
			meta.RevisionID = revisionID
			scenario := sessionmemory.Scenario{Meta: meta, TopicKey: record.TopicKey, Title: record.Title, Summary: record.Text}
			if err := scenario.Validate(); err != nil {
				return sessionmemory.CanonicalBoundaryView{}, err
			}
			view.Snapshot.Scenarios = append(view.Snapshot.Scenarios, scenario)
			legacyRef = sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
		case sessionmemory.DerivedKindProfile:
			itemID := record.LegacyItemID
			if itemID == "" {
				var idErr error
				itemID, idErr = sessionmemory.ProfileItemID(scope)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			meta := sessionmemory.RevisionMeta{SchemaVersion: sessionmemory.DerivedSchemaVersionV1, Kind: kind, ItemID: itemID, Revision: record.Revision, OperationID: operationID, Scope: scope, State: sessionmemory.RevisionStateActive, Provenance: provenance, CreatedAt: record.CreatedAt}
			revisionID := record.LegacyRevisionID
			if revisionID == "" {
				var idErr error
				revisionID, idErr = sessionmemory.DerivedRevisionID(scope, itemID, operationID, []string{record.Text}, provenance, nil)
				if idErr != nil {
					return sessionmemory.CanonicalBoundaryView{}, idErr
				}
			}
			if record.LegacySupersedes != nil {
				copyOf := *record.LegacySupersedes
				meta.Supersedes = &copyOf
			}
			meta.RevisionID = revisionID
			profile := sessionmemory.Profile{Meta: meta, Summary: record.Text}
			if err := profile.Validate(); err != nil {
				return sessionmemory.CanonicalBoundaryView{}, err
			}
			view.Snapshot.Profiles = append(view.Snapshot.Profiles, profile)
			legacyRef = sessionmemory.RevisionRef{ItemID: itemID, RevisionID: revisionID}
		}
		view.LegacyToCanonical[legacyRef] = sessionmemory.RevisionRef{ItemID: record.ItemID, RevisionID: record.RevisionID}
	}
	if err := view.Validate(); err != nil {
		return sessionmemory.CanonicalBoundaryView{}, err
	}
	return view, nil
}
