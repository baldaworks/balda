package sessionmemory

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestScopeViewBuilderKeepsActiveRecentAndChangedReadsBounded(t *testing.T) {
	scope := Scope{Key: "telegram:view", Kind: ScopeKindPersonal}
	store := &boundedProjectionStore{
		state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope, Version: 4, ChangeSeq: 4},
		active: []ActiveCanonicalMemory{
			{Item: projectionViewItem(scope, "item-1"), RevisionID: "revision-1"},
			{Item: projectionViewItem(scope, "item-2"), RevisionID: "revision-2"},
		},
		changes: []ScopeChange{
			{Sequence: 1, OperationID: "operation-1", OccurredAt: projectionViewTime(1), RevisionIDs: []string{"revision-1"}},
			{Sequence: 2, OperationID: "operation-2", OccurredAt: projectionViewTime(2), RevisionIDs: []string{"revision-2"}},
			{Sequence: 3, OperationID: "operation-3", OccurredAt: projectionViewTime(3), RevisionIDs: []string{"revision-3"}},
		},
		revisions: map[string]MemoryRevision{
			"revision-1": projectionViewRevision("revision-1", "item-1", 1),
			"revision-2": projectionViewRevision("revision-2", "item-2", 1),
			"revision-3": projectionViewRevision("revision-3", "item-3", 1),
		},
	}
	builder, err := NewScopeViewBuilder(store)
	if err != nil {
		t.Fatalf("NewScopeViewBuilder() error = %v", err)
	}
	view, err := builder.Build(context.Background(), ProjectionViewRequest{
		Scope:        scope,
		ActiveLimit:  1,
		RecentAfter:  1,
		RecentLimit:  1,
		ChangedAfter: 2,
		ChangedLimit: 1,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if len(view.Active) != 1 || view.Active[0].Item.ItemID != "item-1" {
		t.Fatalf("active view = %#v", view.Active)
	}
	if !view.ActiveTruncated {
		t.Fatal("active view did not report a bounded continuation")
	}
	if len(view.Recent) != 1 || view.Recent[0].RevisionID != "revision-2" {
		t.Fatalf("recent view = %#v", view.Recent)
	}
	if len(view.Changed) != 1 || view.Changed[0].RevisionID != "revision-3" {
		t.Fatalf("changed view = %#v", view.Changed)
	}
	if err := view.Validate(); err != nil {
		t.Fatalf("view.Validate() error = %v", err)
	}
}

func TestProjectionCoordinatorRebuildsWhenRetentionFloorPassesWatermark(t *testing.T) {
	scope := Scope{Key: "telegram:retention", Kind: ScopeKindPersonal}
	canonical := &retentionProjectionStore{
		boundedProjectionStore: boundedProjectionStore{
			state:     ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope, Version: 2, ChangeSeq: 3},
			active:    []ActiveCanonicalMemory{{Item: projectionViewItem(scope, "item-1"), RevisionID: "revision-1"}},
			revisions: map[string]MemoryRevision{"revision-1": projectionViewRevision("revision-1", "item-1", 1)},
		},
		floor: 2,
	}
	checkpoints := &projectionCheckpointStore{manifest: ProjectionManifest{
		Scope: scope, ProjectionID: "bleve", GenerationID: "generation-1", Status: ProjectionGenerationActive,
		Watermark: 1, UpdatedAt: projectionViewTime(1),
	}, found: true}
	applier := &retentionProjectionApplier{}
	coordinator, err := NewProjectionCoordinator(canonical, checkpoints, applier, 2)
	if err != nil {
		t.Fatalf("NewProjectionCoordinator() error = %v", err)
	}
	manifest, err := coordinator.Sync(context.Background(), scope, "bleve", "generation-1")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if !applier.rebuilt || manifest.Status != ProjectionGenerationActive || manifest.Watermark != 3 {
		t.Fatalf("manifest = %+v, rebuilt = %t", manifest, applier.rebuilt)
	}
	if len(applier.view.Active) != 1 || applier.view.Active[0].Item.ItemID != "item-1" {
		t.Fatalf("rebuilt view = %#v", applier.view)
	}
}

func TestProjectionCoordinatorStreamsLargeRetentionRebuildInBoundedBatches(t *testing.T) {
	scope := Scope{Key: "telegram:retention-batches", Kind: ScopeKindPersonal}
	store := &retentionProjectionStore{
		boundedProjectionStore: boundedProjectionStore{
			state: ScopeState{SchemaVersion: CanonicalSchemaVersionV1, Scope: scope, Version: 3, ChangeSeq: 4},
			active: []ActiveCanonicalMemory{
				{Item: projectionViewItem(scope, "item-1"), RevisionID: "revision-1"},
				{Item: projectionViewItem(scope, "item-2"), RevisionID: "revision-2"},
				{Item: projectionViewItem(scope, "item-3"), RevisionID: "revision-3"},
			},
			revisions: map[string]MemoryRevision{
				"revision-1": projectionViewRevision("revision-1", "item-1", 1),
				"revision-2": projectionViewRevision("revision-2", "item-2", 1),
				"revision-3": projectionViewRevision("revision-3", "item-3", 1),
			},
		},
		floor: 2,
	}
	checkpoints := &projectionCheckpointStore{}
	applier := &batchRetentionProjectionApplier{}
	coordinator, err := NewProjectionCoordinator(store, checkpoints, applier, 1)
	if err != nil {
		t.Fatalf("NewProjectionCoordinator() error = %v", err)
	}
	manifest, err := coordinator.Sync(context.Background(), scope, "bleve", "generation-1")
	if err != nil {
		t.Fatalf("Sync() error = %v", err)
	}
	if manifest.Status != ProjectionGenerationActive || manifest.Watermark != 4 {
		t.Fatalf("manifest = %+v", manifest)
	}
	if got, want := applier.batchCount, 3; got != want {
		t.Fatalf("rebuild batch count = %d, want %d", got, want)
	}
	if len(applier.itemIDs) != 3 {
		t.Fatalf("rebuilt item IDs = %#v", applier.itemIDs)
	}
}

func TestScopeCheckpointRecorderIsIdempotentAndMonotonic(t *testing.T) {
	scope := Scope{Key: "telegram:checkpoint", Kind: ScopeKindPersonal}
	store := &memoryCheckpointStore{}
	recorder, err := NewScopeCheckpointRecorder(store)
	if err != nil {
		t.Fatalf("NewScopeCheckpointRecorder() error = %v", err)
	}
	checkpoint := ScopeCheckpoint{
		SchemaVersion: ScopeCheckpointSchemaVersion,
		Scope:         scope,
		Kind:          ScopeCheckpointTurn,
		CheckpointID:  "turn-1",
		ScopeVersion:  2,
		ChangeSeq:     2,
		OccurredAt:    projectionViewTime(2),
		UpdatedAt:     projectionViewTime(2),
	}
	if err := recorder.Record(context.Background(), checkpoint); err != nil {
		t.Fatalf("Record() error = %v", err)
	}
	if err := recorder.Record(context.Background(), checkpoint); err != nil {
		t.Fatalf("Record(replay) error = %v", err)
	}
	checkpoint.CheckpointID = "turn-0"
	checkpoint.ScopeVersion = 1
	checkpoint.ChangeSeq = 1
	if err := recorder.Record(context.Background(), checkpoint); err == nil {
		t.Fatalf("Record(backwards) error = %v, want conflict", err)
	}
}

var errCheckpointBackwards = PermanentError(CodeConflict, "scope checkpoint moved backwards", nil)

type boundedProjectionStore struct {
	CanonicalStore
	state     ScopeState
	active    []ActiveCanonicalMemory
	changes   []ScopeChange
	revisions map[string]MemoryRevision
}

func (s *boundedProjectionStore) LoadScopeState(context.Context, Scope) (ScopeState, error) {
	return s.state, nil
}

func (s *boundedProjectionStore) ScanActiveMemory(_ context.Context, request ActiveMemoryScanRequest) ([]ActiveCanonicalMemory, error) {
	page := make([]ActiveCanonicalMemory, 0, request.Limit)
	for _, active := range s.active {
		if request.AfterItemID != "" && active.Item.ItemID <= request.AfterItemID {
			continue
		}
		page = append(page, active)
		if uint32(len(page)) == request.Limit {
			break
		}
	}
	return page, nil
}

func (s *boundedProjectionStore) ScanScopeChanges(_ context.Context, _ Scope, after uint64, limit uint32) ([]ScopeChange, error) {
	changes := make([]ScopeChange, 0, limit)
	for _, change := range s.changes {
		if change.Sequence <= after {
			continue
		}
		changes = append(changes, change)
		if uint32(len(changes)) == limit {
			break
		}
	}
	return changes, nil
}

func (s *boundedProjectionStore) LoadCanonicalRevisions(_ context.Context, request CanonicalRevisionReadRequest) ([]MemoryRevision, error) {
	revisions := make([]MemoryRevision, 0, len(request.RevisionIDs))
	for _, id := range request.RevisionIDs {
		revision, ok := s.revisions[id]
		if !ok {
			return nil, PermanentError(CodeNotFound, "revision missing", nil)
		}
		revisions = append(revisions, revision)
	}
	return revisions, nil
}

type retentionProjectionStore struct {
	boundedProjectionStore
	floor uint64
}

func (s *retentionProjectionStore) LoadProjectionRetentionFloor(context.Context, Scope, string) (uint64, error) {
	return s.floor, nil
}

type retentionProjectionApplier struct {
	projectionApplier
	rebuilt bool
	view    BoundedProjectionView
}

type batchRetentionProjectionApplier struct {
	batchCount int
	itemIDs    []string
}

func (a *batchRetentionProjectionApplier) BeginProjectionRebuild(context.Context, Scope, string) error {
	a.batchCount = 0
	a.itemIDs = nil
	return nil
}

func (a *batchRetentionProjectionApplier) ApplyProjectionRebuildBatch(_ context.Context, _ Scope, _ string, view BoundedProjectionView) error {
	if len(view.Active) > 1 {
		return errors.New("rebuild batch exceeded configured bound")
	}
	a.batchCount++
	for _, active := range view.Active {
		a.itemIDs = append(a.itemIDs, active.Item.ItemID)
	}
	return nil
}

func (a *batchRetentionProjectionApplier) EndProjectionRebuild(context.Context, Scope, string) error {
	return nil
}

func (a *batchRetentionProjectionApplier) Apply(context.Context, Scope, string, []ScopeChange, []MemoryRevision) error {
	return nil
}

func (a *batchRetentionProjectionApplier) Commit(context.Context, Scope, string) error {
	return nil
}

func (a *retentionProjectionApplier) Rebuild(_ context.Context, _ Scope, _ string, view BoundedProjectionView) error {
	a.rebuilt = true
	a.view = view
	return nil
}

type memoryCheckpointStore struct {
	checkpoint ScopeCheckpoint
	found      bool
}

func (s *memoryCheckpointStore) LoadScopeCheckpoint(context.Context, Scope, ScopeCheckpointKind) (ScopeCheckpoint, bool, error) {
	return s.checkpoint, s.found, nil
}

func (s *memoryCheckpointStore) SaveScopeCheckpoint(_ context.Context, checkpoint ScopeCheckpoint) error {
	if s.found && (checkpoint.ScopeVersion < s.checkpoint.ScopeVersion || checkpoint.ChangeSeq < s.checkpoint.ChangeSeq) {
		return errCheckpointBackwards
	}
	s.checkpoint = checkpoint
	s.found = true
	return nil
}

func projectionViewItem(scope Scope, itemID string) MemoryItem {
	return MemoryItem{ItemID: itemID, Scope: scope, Kind: MemoryKindState, MemoryKey: MemoryKey("key-" + itemID)}
}

func projectionViewRevision(revisionID, itemID string, revision uint64) MemoryRevision {
	return MemoryRevision{
		SchemaVersion: MemorySchemaVersionV2,
		RevisionID:    revisionID,
		ItemID:        itemID,
		Revision:      revision,
		Temporal:      Temporal{ObservedAt: projectionViewTime(int(revision))},
		Evidence:      []EvidenceRef{{SourceID: "source-1", MessageID: "message-1", Role: MessageRoleUser, StartByte: 0, EndByte: 1, AssertionMode: AssertionModeUser}},
		Sensitivity:   SensitivityStandard,
		Retention:     RetentionClassStandard,
		Payload:       PayloadRef{ID: "payload-" + revisionID, KeyID: "key-1", Digest: "digest-1", ByteSize: 1},
	}
}

func projectionViewTime(offset int) time.Time {
	return time.Date(2026, time.August, 6, 0, 0, offset, 0, time.UTC)
}
