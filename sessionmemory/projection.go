package sessionmemory

import (
	"context"
	"fmt"
	"time"
)

// ProjectionGenerationStatus controls whether a rebuild generation is safe to
// advertise. Only Active may serve indexed results; Dirty and Building must be
// rebuilt or resumed from canonical changes.
type ProjectionGenerationStatus string

const (
	ProjectionGenerationBuilding   ProjectionGenerationStatus = "building"
	ProjectionGenerationDirty      ProjectionGenerationStatus = "dirty"
	ProjectionGenerationActive     ProjectionGenerationStatus = "active"
	ProjectionGenerationSuperseded ProjectionGenerationStatus = "superseded"
)

// ProjectionManifest is mutable metadata for one projection generation. The
// canonical change log remains authoritative; Watermark is only the highest
// sequence known to have been applied to this generation.
type ProjectionManifest struct {
	Scope        Scope                      `json:"scope"`
	ProjectionID string                     `json:"projection_id"`
	GenerationID string                     `json:"generation_id"`
	Status       ProjectionGenerationStatus `json:"status"`
	Watermark    uint64                     `json:"watermark"`
	UpdatedAt    time.Time                  `json:"updated_at"`
}

// ProjectionCheckpointStore persists generation manifests independently from
// any concrete projection engine. A coordinator marks a generation Dirty
// before applying a batch, advances the watermark only after the engine commit,
// then atomically activates the clean generation.
type ProjectionCheckpointStore interface {
	LoadProjectionManifest(ctx context.Context, scope Scope, projectionID, generationID string) (ProjectionManifest, bool, error)
	LoadActiveProjectionManifest(ctx context.Context, scope Scope, projectionID string) (ProjectionManifest, bool, error)
	MarkProjectionDirty(ctx context.Context, manifest ProjectionManifest) error
	AdvanceProjectionWatermark(ctx context.Context, manifest ProjectionManifest, watermark uint64, updatedAt time.Time) error
	ActivateProjectionGeneration(ctx context.Context, manifest ProjectionManifest, updatedAt time.Time) error
}

// ProjectionApplier is implemented by a disposable index or materialized-view
// adapter. Apply must be idempotent for a generation/watermark pair; Commit
// makes its preceding batch durable before the checkpoint advances.
type ProjectionApplier interface {
	Apply(ctx context.Context, scope Scope, generationID string, changes []ScopeChange, revisions []MemoryRevision) error
	Commit(ctx context.Context, scope Scope, generationID string) error
}

// ProjectionCoordinator replays canonical changes into one disposable
// projection generation. It never mutates canonical memory and never exposes a
// generation until the projection engine committed the corresponding watermark.
type ProjectionCoordinator struct {
	canonical   CanonicalStore
	checkpoints ProjectionCheckpointStore
	applier     ProjectionApplier
	batchSize   uint32
	now         func() time.Time
}

// NewProjectionCoordinator wires portable canonical and projection ports.
func NewProjectionCoordinator(canonical CanonicalStore, checkpoints ProjectionCheckpointStore, applier ProjectionApplier, batchSize uint32) (*ProjectionCoordinator, error) {
	if canonical == nil || checkpoints == nil || applier == nil {
		return nil, fmt.Errorf("canonical store, projection checkpoints, and applier are required")
	}
	if batchSize == 0 || batchSize > maxCanonicalReadRecords {
		return nil, fmt.Errorf("projection batch size must be between 1 and %d", maxCanonicalReadRecords)
	}
	return &ProjectionCoordinator{canonical: canonical, checkpoints: checkpoints, applier: applier, batchSize: batchSize, now: time.Now}, nil
}

// Sync replays all currently available changes for one scope/generation. A
// failed apply or commit leaves the manifest Dirty, so callers fail closed and
// later retry or rebuild from canonical data.
func (c *ProjectionCoordinator) Sync(ctx context.Context, scope Scope, projectionID, generationID string) (ProjectionManifest, error) {
	if c == nil {
		return ProjectionManifest{}, fmt.Errorf("projection coordinator is required")
	}
	if err := scope.Validate(); err != nil {
		return ProjectionManifest{}, PermanentError(CodePermanent, "projection sync scope is invalid", err)
	}
	if !isCanonicalID(projectionID) || !isCanonicalID(generationID) {
		return ProjectionManifest{}, PermanentError(CodePermanent, "projection sync identity is invalid", nil)
	}
	manifest, found, err := c.checkpoints.LoadProjectionManifest(ctx, scope, projectionID, generationID)
	if err != nil {
		return ProjectionManifest{}, err
	}
	if !found || manifest.GenerationID != generationID {
		manifest = ProjectionManifest{Scope: scope, ProjectionID: projectionID, GenerationID: generationID, Status: ProjectionGenerationBuilding, UpdatedAt: c.currentTime().UTC()}
	}
	if err := c.checkpoints.MarkProjectionDirty(ctx, manifest); err != nil {
		return ProjectionManifest{}, err
	}
	manifest.Status = ProjectionGenerationDirty
	if retention, ok := c.canonical.(ProjectionRetentionFloorReader); ok {
		floor, err := retention.LoadProjectionRetentionFloor(ctx, scope, projectionID)
		if err != nil {
			return manifest, err
		}
		if floor > manifest.Watermark {
			watermark, err := c.rebuildFromCanonical(ctx, scope, generationID)
			if err != nil {
				return manifest, err
			}
			if watermark < floor {
				return manifest, PermanentError(CodeStoreFailure, "projection retention floor is ahead of canonical state", nil)
			}
			if err := c.applier.Commit(ctx, scope, generationID); err != nil {
				return manifest, err
			}
			if err := c.checkpoints.AdvanceProjectionWatermark(ctx, manifest, watermark, c.currentTime().UTC()); err != nil {
				return manifest, err
			}
			manifest.Watermark = watermark
		}
	}
	applied := false
	for {
		changes, err := c.canonical.ScanScopeChanges(ctx, scope, manifest.Watermark, c.batchSize)
		if err != nil {
			return manifest, err
		}
		if len(changes) == 0 {
			break
		}
		ids := uniqueRevisionIDs(changes)
		revisions, err := c.loadRevisions(ctx, scope, ids)
		if err != nil {
			return manifest, err
		}
		if err := c.applier.Apply(ctx, scope, generationID, changes, revisions); err != nil {
			return manifest, err
		}
		watermark := changes[len(changes)-1].Sequence
		if err := c.applier.Commit(ctx, scope, generationID); err != nil {
			return manifest, err
		}
		if err := c.checkpoints.AdvanceProjectionWatermark(ctx, manifest, watermark, c.currentTime().UTC()); err != nil {
			return manifest, err
		}
		manifest.Watermark = watermark
		applied = true
	}
	if !applied {
		if err := c.applier.Commit(ctx, scope, generationID); err != nil {
			return manifest, err
		}
	}
	if err := c.checkpoints.ActivateProjectionGeneration(ctx, manifest, c.currentTime().UTC()); err != nil {
		return manifest, err
	}
	manifest.Status = ProjectionGenerationActive
	return manifest, nil
}

func (c *ProjectionCoordinator) rebuildFromCanonical(ctx context.Context, scope Scope, generationID string) (uint64, error) {
	builder, err := NewScopeViewBuilder(c.canonical)
	if err != nil {
		return 0, err
	}
	if rebuilder, ok := c.applier.(ProjectionBatchRebuildApplier); ok {
		if err := rebuilder.BeginProjectionRebuild(ctx, scope, generationID); err != nil {
			return 0, err
		}
		after := ""
		var watermark uint64
		for {
			view, err := builder.Build(ctx, ProjectionViewRequest{
				Scope:             scope,
				ActiveAfterItemID: after,
				ActiveLimit:       c.batchSize,
				ActiveOnly:        true,
			})
			if err != nil {
				return 0, err
			}
			watermark = view.State.ChangeSeq
			if err := rebuilder.ApplyProjectionRebuildBatch(ctx, scope, generationID, view); err != nil {
				return 0, err
			}
			if len(view.Active) == 0 || !view.ActiveTruncated {
				break
			}
			after = view.Active[len(view.Active)-1].Item.ItemID
		}
		if err := rebuilder.EndProjectionRebuild(ctx, scope, generationID); err != nil {
			return 0, err
		}
		return watermark, nil
	}
	rebuilder, ok := c.applier.(ProjectionRebuildApplier)
	if !ok {
		return 0, PermanentError(CodeConflict, "projection watermark is below the retention floor and requires a rebuild", nil)
	}
	view, err := builder.Build(ctx, ProjectionViewRequest{Scope: scope, ActiveLimit: maxCanonicalReadRecords})
	if err != nil {
		return 0, err
	}
	if view.ActiveTruncated {
		return 0, PermanentError(CodeLimitExceeded, "projection rebuild requires a batch-capable applier", nil)
	}
	if err := rebuilder.Rebuild(ctx, scope, generationID, view); err != nil {
		return 0, err
	}
	return view.State.ChangeSeq, nil
}

func (c *ProjectionCoordinator) loadRevisions(ctx context.Context, scope Scope, ids []string) ([]MemoryRevision, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	revisions := make([]MemoryRevision, 0, len(ids))
	for start := 0; start < len(ids); start += maxCanonicalReadRecords {
		end := start + maxCanonicalReadRecords
		if end > len(ids) {
			end = len(ids)
		}
		batch, err := c.canonical.LoadCanonicalRevisions(ctx, CanonicalRevisionReadRequest{Scope: scope, RevisionIDs: ids[start:end]})
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, batch...)
	}
	return revisions, nil
}

func (c *ProjectionCoordinator) currentTime() time.Time {
	if c.now == nil {
		return time.Now()
	}
	return c.now()
}

func uniqueRevisionIDs(changes []ScopeChange) []string {
	seen := make(map[string]struct{})
	ids := make([]string, 0)
	for _, change := range changes {
		for _, id := range change.RevisionIDs {
			if _, ok := seen[id]; !ok {
				seen[id] = struct{}{}
				ids = append(ids, id)
			}
		}
	}
	return ids
}

// Validate verifies a projection manifest does not expose an ambiguous
// generation or claim an uninitialized state.
func (m ProjectionManifest) Validate() error {
	if err := m.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(m.ProjectionID) || !isCanonicalID(m.GenerationID) || m.UpdatedAt.IsZero() {
		return invalidDerived("projection manifest identity is invalid")
	}
	switch m.Status {
	case ProjectionGenerationBuilding, ProjectionGenerationDirty, ProjectionGenerationActive, ProjectionGenerationSuperseded:
		return nil
	default:
		return invalidDerived("projection manifest status is invalid")
	}
}
