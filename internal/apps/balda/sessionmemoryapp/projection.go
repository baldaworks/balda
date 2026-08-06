package sessionmemoryapp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

const defaultProjectionBatchSize = 128

// projectionRebuildStarter is deliberately local to the application wiring:
// the portable coordinator only needs ProjectionApplier, while Bleve needs a
// generation created before a no-change sync can commit an empty generation.
type projectionRebuildStarter interface {
	BeginProjectionRebuild(ctx context.Context, scope sessionmemory.Scope, generationID string) error
}

// ProjectionRuntime owns the rebuildable Bleve projection lifecycle for the
// canonical provider. Every sync builds a fresh generation from canonical
// changes, commits it, advances the durable watermark, and activates it only
// after the commit. Canonical state remains authoritative if projection work
// fails.
type ProjectionRuntime struct {
	coordinator *sessionmemory.ProjectionCoordinator
	starter     projectionRebuildStarter
	projection  string

	mu      sync.Mutex
	active  sync.WaitGroup
	serial  uint64
	closing bool
}

func NewProjectionRuntime(canonical sessionmemory.CanonicalStore, checkpoints sessionmemory.ProjectionCheckpointStore, applier sessionmemory.ProjectionApplier, projectionID string, batchSize uint32) (*ProjectionRuntime, error) {
	if canonical == nil || checkpoints == nil || applier == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "projection runtime dependencies are required", nil)
	}
	if projectionID == "" {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "projection runtime identity is required", nil)
	}
	if batchSize == 0 {
		batchSize = defaultProjectionBatchSize
	}
	coordinator, err := sessionmemory.NewProjectionCoordinator(canonical, checkpoints, applier, batchSize)
	if err != nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "create projection coordinator", err)
	}
	runtime := &ProjectionRuntime{coordinator: coordinator, projection: projectionID}
	if starter, ok := applier.(projectionRebuildStarter); ok {
		runtime.starter = starter
	}
	return runtime, nil
}

// Sync publishes one exact-scope projection generation. Only lifecycle and
// generation identity bookkeeping are serialized; the actual canonical replay
// for unrelated scopes can proceed independently.
func (r *ProjectionRuntime) Sync(ctx context.Context, scope sessionmemory.Scope) error {
	if r == nil || r.coordinator == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "projection runtime is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "projection runtime context is required", nil)
	}
	if err := scope.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	if r.closing {
		r.mu.Unlock()
		return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "projection runtime is closing", nil)
	}
	r.serial++
	serial := r.serial
	r.active.Add(1)
	r.mu.Unlock()
	defer r.active.Done()
	generationID := projectionGenerationID(r.projection, scope, serial, time.Now().UTC())
	if r.starter != nil {
		if err := r.starter.BeginProjectionRebuild(ctx, scope, generationID); err != nil {
			return err
		}
	}
	_, err := r.coordinator.Sync(ctx, scope, r.projection, generationID)
	return err
}

// Close prevents new syncs. The underlying applier owns generation/index
// closure and is intentionally closed by the composition root in dependency
// order after ingress has stopped.
func (r *ProjectionRuntime) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.closing = true
	r.mu.Unlock()
	r.active.Wait()
}

func projectionGenerationID(projectionID string, scope sessionmemory.Scope, serial uint64, now time.Time) string {
	hash := sha256.New()
	_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\x00%d\x00%d", projectionID, scope.Key, scope.Kind, serial, now.UnixNano())
	return "session-memory:v2:projection:" + hex.EncodeToString(hash.Sum(nil))
}
