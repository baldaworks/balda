package state

import (
	"context"
	"sync"

	"github.com/normahq/balda/sessionmemory"
)

// BleveCanonicalApplier adapts bounded canonical revisions to disposable
// Bleve generations. It never treats index content as authoritative: every
// revision is hydrated through the canonical reader before indexing.
type BleveCanonicalApplier struct {
	projection *BleveRecallProjection
	reader     sessionmemory.RecallCanonicalReader

	mu          sync.Mutex
	generations map[string]*BleveGeneration
}

func NewBleveCanonicalApplier(projection *BleveRecallProjection, reader sessionmemory.RecallCanonicalReader) (*BleveCanonicalApplier, error) {
	if projection == nil || reader == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve canonical applier dependencies are required", nil)
	}
	return &BleveCanonicalApplier{projection: projection, reader: reader, generations: make(map[string]*BleveGeneration)}, nil
}

// ScrubCanonicalForget removes matching disposable projection material after
// the canonical deny/CAS decision has committed.  Recall remains correct if
// this best-effort maintenance hook is interrupted because every candidate is
// revalidated against canonical denial state.
func (a *BleveCanonicalApplier) ScrubCanonicalForget(ctx context.Context, scope sessionmemory.Scope, sourceIDs, revisionIDs []string) error {
	if a == nil || a.projection == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "bleve canonical applier is unavailable", nil)
	}
	return a.projection.ScrubCanonicalForget(ctx, scope, sourceIDs, revisionIDs)
}

var _ sessionmemory.ProjectionApplier = (*BleveCanonicalApplier)(nil)
var _ sessionmemory.ProjectionBatchRebuildApplier = (*BleveCanonicalApplier)(nil)
var _ sessionmemory.ProjectionGenerationActivator = (*BleveCanonicalApplier)(nil)

func (a *BleveCanonicalApplier) generation(ctx context.Context, id string) (*BleveGeneration, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if generation := a.generations[id]; generation != nil {
		return generation, nil
	}
	generation, err := a.projection.NewGeneration(id)
	if err != nil {
		return nil, err
	}
	a.generations[id] = generation
	return generation, nil
}

func (a *BleveCanonicalApplier) Apply(ctx context.Context, scope sessionmemory.Scope, generationID string, _ []sessionmemory.ScopeChange, revisions []sessionmemory.MemoryRevision) error {
	if err := scope.Validate(); err != nil {
		return err
	}
	generation, err := a.generation(ctx, generationID)
	if err != nil {
		return err
	}
	if len(revisions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(revisions))
	for _, revision := range revisions {
		ids = append(ids, revision.RevisionID)
	}
	records, err := a.reader.LoadRecallRecords(ctx, scope, ids)
	if err != nil {
		return err
	}
	byID := make(map[string]sessionmemory.RecallRecord, len(records))
	for _, record := range records {
		byID[record.RevisionID] = record
	}
	for _, revision := range revisions {
		record, ok := byID[revision.RevisionID]
		if !ok {
			if err := generation.Delete(ctx, revision.RevisionID); err != nil {
				return err
			}
			continue
		}
		category := record.Category
		document := sessionmemory.RecallProjectionDocument{
			Scope: record.Scope, ItemID: record.ItemID, RevisionID: record.RevisionID,
			Revision: record.Revision, Kind: record.Kind, Category: category,
			MemoryKey: record.MemoryKey, Text: record.Text, CreatedAt: record.CreatedAt,
			Temporal: record.Temporal, Sensitivity: record.Sensitivity, Retention: record.Retention,
			SourceIDs: append([]string(nil), record.SourceIDs...), SessionIDs: append([]string(nil), record.SessionIDs...),
			ScopeChangeSeq: record.ScopeChangeSeq,
		}
		if err := generation.Index(ctx, document); err != nil {
			return err
		}
	}
	return nil
}

func (a *BleveCanonicalApplier) Commit(ctx context.Context, _ sessionmemory.Scope, generationID string) error {
	a.mu.Lock()
	generation := a.generations[generationID]
	a.mu.Unlock()
	if generation == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "bleve generation is not initialized", nil)
	}
	return generation.Commit(ctx)
}

func (a *BleveCanonicalApplier) Activate(ctx context.Context, scope sessionmemory.Scope, generationID string) error {
	if err := a.projection.ActivateGenerationForScope(ctx, scope, generationID); err != nil {
		return err
	}
	a.mu.Lock()
	generation := a.generations[generationID]
	delete(a.generations, generationID)
	a.mu.Unlock()
	return generation.Close()
}

func (a *BleveCanonicalApplier) BeginProjectionRebuild(ctx context.Context, _ sessionmemory.Scope, generationID string) error {
	_, err := a.generation(ctx, generationID)
	return err
}

func (a *BleveCanonicalApplier) ApplyProjectionRebuildBatch(ctx context.Context, scope sessionmemory.Scope, generationID string, view sessionmemory.BoundedProjectionView) error {
	if err := view.Validate(); err != nil {
		return err
	}
	revisions := make([]sessionmemory.MemoryRevision, 0, len(view.Active))
	for _, active := range view.Active {
		revisions = append(revisions, active.Revision)
	}
	return a.Apply(ctx, scope, generationID, nil, revisions)
}

func (a *BleveCanonicalApplier) EndProjectionRebuild(context.Context, sessionmemory.Scope, string) error {
	return nil
}

// Close closes unactivated build generations and the active projection.
func (a *BleveCanonicalApplier) Close() error {
	a.mu.Lock()
	generations := make([]*BleveGeneration, 0, len(a.generations))
	for id, generation := range a.generations {
		generations = append(generations, generation)
		delete(a.generations, id)
	}
	a.mu.Unlock()
	var first error
	for _, generation := range generations {
		if err := generation.Close(); err != nil && first == nil {
			first = err
		}
	}
	if err := a.projection.Close(); err != nil && first == nil {
		first = err
	}
	return first
}
