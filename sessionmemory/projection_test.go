package sessionmemory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestProjectionManifestValidation(t *testing.T) {
	manifest := ProjectionManifest{
		Scope:        Scope{Key: "telegram:projection", Kind: ScopeKindPersonal},
		ProjectionID: "bleve",
		GenerationID: "generation-1",
		Status:       ProjectionGenerationBuilding,
		UpdatedAt:    time.Date(2026, time.August, 6, 0, 0, 0, 0, time.UTC),
	}
	if err := manifest.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	manifest.Status = "unknown"
	if err := manifest.Validate(); err == nil {
		t.Fatal("Validate() error = nil for unknown status")
	}
}

func TestProjectionCoordinatorActivatesOnlyAfterAllBatchesCommit(t *testing.T) {
	scope := Scope{Key: "telegram:projection", Kind: ScopeKindPersonal}
	canonical := &projectionCanonicalStore{changes: []ScopeChange{
		{Sequence: 1, OperationID: "operation-1", RevisionIDs: []string{"revision-1"}},
		{Sequence: 2, OperationID: "operation-2", RevisionIDs: []string{"revision-2"}},
	}}
	checkpoints := &projectionCheckpointStore{}
	applier := &projectionApplier{}
	coordinator, err := NewProjectionCoordinator(canonical, checkpoints, applier, 1)
	if err != nil {
		t.Fatalf("NewProjectionCoordinator() error = %v", err)
	}
	coordinator.now = func() time.Time { return time.Date(2026, time.August, 6, 1, 0, 0, 0, time.UTC) }

	manifest, err := coordinator.Sync(context.Background(), scope, "bleve", "generation-1")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if manifest.Status != ProjectionGenerationActive || manifest.Watermark != 2 {
		t.Fatalf("manifest = %+v, want active at watermark 2", manifest)
	}
	if got, want := applier.applyCount, 2; got != want {
		t.Fatalf("apply count = %d, want %d", got, want)
	}
	if got, want := applier.commitCount, 2; got != want {
		t.Fatalf("commit count = %d, want %d", got, want)
	}
	if checkpoints.manifest.Status != ProjectionGenerationActive || checkpoints.manifest.Watermark != 2 {
		t.Fatalf("checkpoint = %+v, want active at watermark 2", checkpoints.manifest)
	}
}

func TestProjectionCoordinatorLeavesGenerationDirtyWhenApplyFails(t *testing.T) {
	scope := Scope{Key: "telegram:projection", Kind: ScopeKindPersonal}
	canonical := &projectionCanonicalStore{changes: []ScopeChange{{Sequence: 1, OperationID: "operation-1", RevisionIDs: []string{"revision-1"}}}}
	checkpoints := &projectionCheckpointStore{}
	applier := &projectionApplier{applyErr: errors.New("index unavailable")}
	coordinator, err := NewProjectionCoordinator(canonical, checkpoints, applier, 1)
	if err != nil {
		t.Fatalf("NewProjectionCoordinator() error = %v", err)
	}

	if _, err := coordinator.Sync(context.Background(), scope, "bleve", "generation-1"); !errors.Is(err, applier.applyErr) {
		t.Fatalf("Sync() error = %v, want apply failure", err)
	}
	if checkpoints.manifest.Status != ProjectionGenerationDirty {
		t.Fatalf("checkpoint status = %q, want dirty", checkpoints.manifest.Status)
	}
	if checkpoints.activateCount != 0 || checkpoints.manifest.Watermark != 0 {
		t.Fatalf("failed generation was exposed: %+v", checkpoints.manifest)
	}
}

type projectionCanonicalStore struct {
	CanonicalStore
	changes []ScopeChange
}

func (s *projectionCanonicalStore) ScanScopeChanges(_ context.Context, _ Scope, after uint64, limit uint32) ([]ScopeChange, error) {
	changes := make([]ScopeChange, 0, limit)
	for _, change := range s.changes {
		if change.Sequence > after && uint32(len(changes)) < limit {
			changes = append(changes, change)
		}
	}
	return changes, nil
}

func (s *projectionCanonicalStore) LoadCanonicalRevisions(_ context.Context, request CanonicalRevisionReadRequest) ([]MemoryRevision, error) {
	revisions := make([]MemoryRevision, 0, len(request.RevisionIDs))
	for _, id := range request.RevisionIDs {
		revisions = append(revisions, MemoryRevision{RevisionID: id})
	}
	return revisions, nil
}

type projectionCheckpointStore struct {
	manifest      ProjectionManifest
	found         bool
	activateCount int
}

func (s *projectionCheckpointStore) LoadProjectionManifest(_ context.Context, _ Scope, _ string, _ string) (ProjectionManifest, bool, error) {
	return s.manifest, s.found, nil
}

func (s *projectionCheckpointStore) LoadActiveProjectionManifest(_ context.Context, _ Scope, _ string) (ProjectionManifest, bool, error) {
	return s.manifest, s.found && s.manifest.Status == ProjectionGenerationActive, nil
}

func (s *projectionCheckpointStore) MarkProjectionDirty(_ context.Context, manifest ProjectionManifest) error {
	manifest.Status = ProjectionGenerationDirty
	s.manifest = manifest
	s.found = true
	return nil
}

func (s *projectionCheckpointStore) AdvanceProjectionWatermark(_ context.Context, manifest ProjectionManifest, watermark uint64, updatedAt time.Time) error {
	s.manifest = manifest
	s.manifest.Status = ProjectionGenerationDirty
	s.manifest.Watermark = watermark
	s.manifest.UpdatedAt = updatedAt
	return nil
}

func (s *projectionCheckpointStore) ActivateProjectionGeneration(_ context.Context, manifest ProjectionManifest, updatedAt time.Time) error {
	s.manifest = manifest
	s.manifest.Status = ProjectionGenerationActive
	s.manifest.UpdatedAt = updatedAt
	s.activateCount++
	return nil
}

type projectionApplier struct {
	applyCount  int
	commitCount int
	applyErr    error
}

func (a *projectionApplier) Apply(_ context.Context, _ Scope, _ string, _ []ScopeChange, _ []MemoryRevision) error {
	a.applyCount++
	return a.applyErr
}

func (a *projectionApplier) Commit(_ context.Context, _ Scope, _ string) error {
	a.commitCount++
	return nil
}
