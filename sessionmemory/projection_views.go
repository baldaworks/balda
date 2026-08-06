package sessionmemory

import (
	"context"
	"fmt"
	"time"
)

const (
	// ProjectionViewSchemaVersion identifies the bounded canonical view passed
	// to rebuildable materialized projections.
	ProjectionViewSchemaVersion = "session-memory-projection-view/v1"

	// ScopeCheckpointSchemaVersion identifies the durable per-scope checkpoint
	// record. Checkpoints are cursors, not canonical memory state.
	ScopeCheckpointSchemaVersion = "session-memory-scope-checkpoint/v1"

	defaultProjectionActiveLimit  uint32 = 128
	defaultProjectionRecentLimit  uint32 = 64
	defaultProjectionChangedLimit uint32 = 64
)

// ProjectionRetentionFloorReader reports the minimum watermark that remains
// safe for incremental replay after retention removed older change records. A
// floor greater than a generation's watermark requires a complete rebuild
// from canonical active state.
// Implementations may omit this optional port when change records are retained
// indefinitely.
type ProjectionRetentionFloorReader interface {
	LoadProjectionRetentionFloor(ctx context.Context, scope Scope, projectionID string) (uint64, error)
}

// ProjectionRetentionFloorWriter is the maintenance-side port for advancing
// the replay floor after an explicitly verified retention operation.
type ProjectionRetentionFloorWriter interface {
	ProjectionRetentionFloorReader
	SetProjectionRetentionFloor(ctx context.Context, scope Scope, projectionID string, floor uint64) error
}

// ProjectionRebuildApplier is an optional extension implemented by a
// disposable projection. Rebuild must replace the generation from the bounded
// canonical view; it must not advertise the generation until Commit returns.
type ProjectionRebuildApplier interface {
	Rebuild(ctx context.Context, scope Scope, generationID string, view BoundedProjectionView) error
}

// ProjectionBatchRebuildApplier is the streaming rebuild extension for large
// scopes. Each batch is bounded by the canonical read limit; the applier must
// keep the generation unadvertised until the coordinator calls Commit.
type ProjectionBatchRebuildApplier interface {
	BeginProjectionRebuild(ctx context.Context, scope Scope, generationID string) error
	ApplyProjectionRebuildBatch(ctx context.Context, scope Scope, generationID string, view BoundedProjectionView) error
	EndProjectionRebuild(ctx context.Context, scope Scope, generationID string) error
}

// ProjectionViewRequest controls the bounded canonical records used by a
// materialized projection rebuild or synthesis call.
type ProjectionViewRequest struct {
	Scope             Scope  `json:"scope"`
	ActiveAfterItemID string `json:"active_after_item_id,omitempty"`
	ActiveLimit       uint32 `json:"active_limit"`
	RecentAfter       uint64 `json:"recent_after"`
	RecentLimit       uint32 `json:"recent_limit"`
	ChangedAfter      uint64 `json:"changed_after"`
	ChangedLimit      uint32 `json:"changed_limit"`
	ActiveOnly        bool   `json:"active_only,omitempty"`
}

func (r ProjectionViewRequest) normalized() (ProjectionViewRequest, error) {
	if err := r.Scope.Validate(); err != nil {
		return ProjectionViewRequest{}, err
	}
	if r.ActiveAfterItemID != "" && !isCanonicalID(r.ActiveAfterItemID) {
		return ProjectionViewRequest{}, invalidDerived("projection active cursor is invalid")
	}
	out := r
	if out.ActiveLimit == 0 {
		out.ActiveLimit = defaultProjectionActiveLimit
	}
	if out.ActiveOnly {
		out.RecentLimit = 0
		out.ChangedLimit = 0
	} else if out.RecentLimit == 0 {
		out.RecentLimit = defaultProjectionRecentLimit
	}
	if !out.ActiveOnly && out.ChangedLimit == 0 {
		out.ChangedLimit = defaultProjectionChangedLimit
	}
	for _, limit := range []uint32{out.ActiveLimit, out.RecentLimit, out.ChangedLimit} {
		if limit > maxCanonicalReadRecords {
			return ProjectionViewRequest{}, limitExceeded("projection view limit exceeds the canonical read bound")
		}
	}
	return out, nil
}

// ActiveProjectionRevision joins an active item to its current canonical
// revision. It intentionally contains metadata and evidence only; payload
// content remains behind the canonical storage port.
type ActiveProjectionRevision struct {
	Item     MemoryItem     `json:"item"`
	Revision MemoryRevision `json:"revision"`
}

// BoundedProjectionView is the rebuildable projection input. Canonical state
// and revisions remain authoritative; every slice is bounded independently.
type BoundedProjectionView struct {
	SchemaVersion   string                     `json:"schema_version"`
	State           ScopeState                 `json:"state"`
	Active          []ActiveProjectionRevision `json:"active,omitempty"`
	ActiveTruncated bool                       `json:"active_truncated,omitempty"`
	Recent          []MemoryRevision           `json:"recent,omitempty"`
	Changed         []MemoryRevision           `json:"changed,omitempty"`
}

// Validate verifies exact scope ownership, canonical record shape, and the
// bounded nature of the materialized view.
func (v BoundedProjectionView) Validate() error {
	if v.SchemaVersion != ProjectionViewSchemaVersion {
		return invalidDerived("projection view schema version is invalid")
	}
	if err := v.State.Validate(); err != nil {
		return err
	}
	if len(v.Active) > maxCanonicalReadRecords || len(v.Recent) > maxCanonicalReadRecords || len(v.Changed) > maxCanonicalReadRecords {
		return limitExceeded("projection view exceeds the canonical read bound")
	}
	seenActive := make(map[string]struct{}, len(v.Active))
	for _, active := range v.Active {
		if err := active.Item.Validate(); err != nil {
			return err
		}
		if active.Item.Scope != v.State.Scope || active.Revision.ItemID != active.Item.ItemID || active.Revision.RevisionID == "" {
			return PermanentError(CodeScopeViolation, "projection active revision does not match the scope", nil)
		}
		if err := active.Revision.Validate(); err != nil {
			return err
		}
		if _, exists := seenActive[active.Item.ItemID]; exists {
			return invalidDerived("projection view contains duplicate active items")
		}
		seenActive[active.Item.ItemID] = struct{}{}
	}
	for _, revision := range append(append([]MemoryRevision(nil), v.Recent...), v.Changed...) {
		if err := revision.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// ScopeViewBuilder reads canonical state in bounded pages. It never loads a
// full scope and is suitable for Scenario/Profile or retrieval projection
// rebuilds.
type ScopeViewBuilder struct {
	canonical CanonicalStore
}

// NewScopeViewBuilder constructs a storage-neutral bounded-view builder.
func NewScopeViewBuilder(canonical CanonicalStore) (*ScopeViewBuilder, error) {
	if canonical == nil {
		return nil, fmt.Errorf("canonical store is required")
	}
	return &ScopeViewBuilder{canonical: canonical}, nil
}

// Build returns active heads plus bounded recent and changed revisions. The
// request's cursors are exact canonical change sequences, so callers can
// resume a view without relying on wall-clock timestamps.
func (b *ScopeViewBuilder) Build(ctx context.Context, request ProjectionViewRequest) (BoundedProjectionView, error) {
	if b == nil || b.canonical == nil {
		return BoundedProjectionView{}, fmt.Errorf("scope view builder is required")
	}
	normalized, err := request.normalized()
	if err != nil {
		return BoundedProjectionView{}, err
	}
	state, err := b.canonical.LoadScopeState(ctx, normalized.Scope)
	if err != nil {
		return BoundedProjectionView{}, err
	}
	if state.Scope != normalized.Scope {
		return BoundedProjectionView{}, PermanentError(CodeScopeViolation, "canonical scope state does not match the projection view", nil)
	}
	view := BoundedProjectionView{SchemaVersion: ProjectionViewSchemaVersion, State: state}
	view.Active, view.ActiveTruncated, err = b.loadActive(ctx, normalized.Scope, normalized.ActiveAfterItemID, normalized.ActiveLimit)
	if err != nil {
		return BoundedProjectionView{}, err
	}
	recentAfter := normalized.RecentAfter
	if recentAfter == 0 && state.ChangeSeq > uint64(normalized.RecentLimit) {
		recentAfter = state.ChangeSeq - uint64(normalized.RecentLimit)
	}
	view.Recent, err = b.loadChanges(ctx, normalized.Scope, recentAfter, normalized.RecentLimit)
	if err != nil {
		return BoundedProjectionView{}, err
	}
	view.Changed, err = b.loadChanges(ctx, normalized.Scope, normalized.ChangedAfter, normalized.ChangedLimit)
	if err != nil {
		return BoundedProjectionView{}, err
	}
	if err := view.Validate(); err != nil {
		return BoundedProjectionView{}, err
	}
	return view, nil
}

func (b *ScopeViewBuilder) loadActive(ctx context.Context, scope Scope, after string, limit uint32) ([]ActiveProjectionRevision, bool, error) {
	active := make([]ActiveProjectionRevision, 0, limit)
	for uint32(len(active)) < limit {
		pageLimit := limit - uint32(len(active))
		if pageLimit > maxCanonicalReadRecords {
			pageLimit = maxCanonicalReadRecords
		}
		page, err := b.canonical.ScanActiveMemory(ctx, ActiveMemoryScanRequest{Scope: scope, AfterItemID: after, Limit: pageLimit})
		if err != nil {
			return nil, false, err
		}
		if len(page) == 0 {
			break
		}
		ids := make([]string, 0, len(page))
		for _, item := range page {
			if item.Item.Scope != scope || item.RevisionID == "" {
				return nil, false, PermanentError(CodeScopeViolation, "canonical active memory has the wrong scope", nil)
			}
			ids = append(ids, item.RevisionID)
		}
		revisions, err := b.loadRevisionIDs(ctx, scope, ids)
		if err != nil {
			return nil, false, err
		}
		byID := make(map[string]MemoryRevision, len(revisions))
		for _, revision := range revisions {
			byID[revision.RevisionID] = revision
		}
		for _, item := range page {
			revision, ok := byID[item.RevisionID]
			if !ok || revision.ItemID != item.Item.ItemID {
				return nil, false, PermanentError(CodeStoreFailure, "canonical active revision is missing", nil)
			}
			active = append(active, ActiveProjectionRevision{Item: item.Item, Revision: revision})
		}
		after = page[len(page)-1].Item.ItemID
		if uint32(len(page)) < pageLimit {
			break
		}
	}
	if len(active) == 0 || uint32(len(active)) < limit {
		return active, false, nil
	}
	probe, err := b.canonical.ScanActiveMemory(ctx, ActiveMemoryScanRequest{Scope: scope, AfterItemID: active[len(active)-1].Item.ItemID, Limit: 1})
	if err != nil {
		return nil, false, err
	}
	return active, len(probe) != 0, nil
}

func (b *ScopeViewBuilder) loadChanges(ctx context.Context, scope Scope, after uint64, limit uint32) ([]MemoryRevision, error) {
	if limit == 0 {
		return nil, nil
	}
	changes, err := b.canonical.ScanScopeChanges(ctx, scope, after, limit)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		for _, id := range change.RevisionIDs {
			if _, exists := seen[id]; exists {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
		}
	}
	return b.loadRevisionIDs(ctx, scope, ids)
}

func (b *ScopeViewBuilder) loadRevisionIDs(ctx context.Context, scope Scope, ids []string) ([]MemoryRevision, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	revisions := make([]MemoryRevision, 0, len(ids))
	for start := 0; start < len(ids); start += maxCanonicalReadRecords {
		end := start + maxCanonicalReadRecords
		if end > len(ids) {
			end = len(ids)
		}
		batch, err := b.canonical.LoadCanonicalRevisions(ctx, CanonicalRevisionReadRequest{Scope: scope, RevisionIDs: ids[start:end]})
		if err != nil {
			return nil, err
		}
		revisions = append(revisions, batch...)
	}
	return revisions, nil
}

// ScopeCheckpointKind identifies the lifecycle trigger that produced a
// checkpoint. All kinds use one exact scope key and are persisted separately.
type ScopeCheckpointKind string

const (
	ScopeCheckpointTurn     ScopeCheckpointKind = "turn"
	ScopeCheckpointToken    ScopeCheckpointKind = "token"
	ScopeCheckpointTime     ScopeCheckpointKind = "time"
	ScopeCheckpointBoundary ScopeCheckpointKind = "boundary"
)

func (k ScopeCheckpointKind) Validate() error {
	switch k {
	case ScopeCheckpointTurn, ScopeCheckpointToken, ScopeCheckpointTime, ScopeCheckpointBoundary:
		return nil
	default:
		return invalidDerived("unsupported scope checkpoint kind")
	}
}

// ScopeCheckpoint is the durable per-scope cursor used by synthesis and
// projection workers. It is intentionally independent from transport types.
type ScopeCheckpoint struct {
	SchemaVersion string              `json:"schema_version"`
	Scope         Scope               `json:"scope"`
	Kind          ScopeCheckpointKind `json:"kind"`
	CheckpointID  string              `json:"checkpoint_id"`
	ScopeVersion  uint64              `json:"scope_version"`
	ChangeSeq     uint64              `json:"change_seq"`
	TokenCount    uint64              `json:"token_count,omitempty"`
	OccurredAt    time.Time           `json:"occurred_at"`
	UpdatedAt     time.Time           `json:"updated_at"`
}

func (c ScopeCheckpoint) Validate() error {
	if c.SchemaVersion != ScopeCheckpointSchemaVersion {
		return invalidDerived("scope checkpoint schema version is invalid")
	}
	if err := c.Scope.Validate(); err != nil {
		return err
	}
	if err := c.Kind.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(c.CheckpointID) || c.OccurredAt.IsZero() || c.UpdatedAt.IsZero() {
		return invalidDerived("scope checkpoint identity or timestamp is invalid")
	}
	if c.Kind != ScopeCheckpointToken && c.TokenCount != 0 {
		return invalidDerived("non-token checkpoint contains a token count")
	}
	return nil
}

// ScopeCheckpointStore persists the latest checkpoint for each exact scope
// and trigger kind. Implementations must make a checkpoint update durable
// before returning and reject backwards cursors.
type ScopeCheckpointStore interface {
	LoadScopeCheckpoint(ctx context.Context, scope Scope, kind ScopeCheckpointKind) (ScopeCheckpoint, bool, error)
	SaveScopeCheckpoint(ctx context.Context, checkpoint ScopeCheckpoint) error
}

// ScopeCheckpointRecorder is the lane-owned adapter used by turn, token,
// timer, and boundary triggers. It makes replay of the same cursor idempotent
// and rejects out-of-order writes before they reach durable storage.
type ScopeCheckpointRecorder struct {
	store ScopeCheckpointStore
}

func NewScopeCheckpointRecorder(store ScopeCheckpointStore) (*ScopeCheckpointRecorder, error) {
	if store == nil {
		return nil, fmt.Errorf("scope checkpoint store is required")
	}
	return &ScopeCheckpointRecorder{store: store}, nil
}

func (r *ScopeCheckpointRecorder) Record(ctx context.Context, checkpoint ScopeCheckpoint) error {
	if r == nil || r.store == nil {
		return fmt.Errorf("scope checkpoint recorder is required")
	}
	if err := checkpoint.Validate(); err != nil {
		return err
	}
	prior, found, err := r.store.LoadScopeCheckpoint(ctx, checkpoint.Scope, checkpoint.Kind)
	if err != nil {
		return err
	}
	if found {
		if err := prior.Validate(); err != nil {
			return err
		}
		if prior.Scope != checkpoint.Scope || prior.Kind != checkpoint.Kind {
			return PermanentError(CodeScopeViolation, "scope checkpoint ownership changed", nil)
		}
		if checkpoint.ScopeVersion < prior.ScopeVersion || checkpoint.ChangeSeq < prior.ChangeSeq || checkpoint.OccurredAt.Before(prior.OccurredAt) {
			return PermanentError(CodeConflict, "scope checkpoint moved backwards", nil)
		}
		if checkpoint.ScopeVersion == prior.ScopeVersion && checkpoint.ChangeSeq == prior.ChangeSeq && checkpoint.CheckpointID == prior.CheckpointID {
			return nil
		}
	}
	return r.store.SaveScopeCheckpoint(ctx, checkpoint)
}
