package state

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

const stateTestRevisionOneID = "revision-1"

func TestOptionalVecgoRecallProjectionGenerationCommitAndReopen(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	root := t.TempDir()
	config := VecgoRecallProjectionConfig{Enabled: true, ModelVersion: "embed-v1", Dimension: 2, Metric: "cosine"}
	projection, err := OpenOptionalVecgoRecallProjection(ctx, root, config)
	if err != nil {
		t.Fatalf("OpenOptionalVecgoRecallProjection() error = %v", err)
	}
	if projection == nil {
		t.Fatal("OpenOptionalVecgoRecallProjection() returned nil for enabled config")
	}
	generation, err := projection.NewGeneration(ctx, "generation-1")
	if err != nil {
		t.Fatalf("NewGeneration() error = %v", err)
	}
	if err := generation.Index(ctx, validVecgoDocument(stateTestRevisionOneID, "item-1", []float32{1, 0})); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if err := generation.Commit(ctx); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := generation.AdvanceWatermark(ctx, 4); err != nil {
		t.Fatalf("AdvanceWatermark() error = %v", err)
	}
	if err := projection.ActivateGeneration(ctx, "generation-1"); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}
	if err := generation.Close(); err != nil {
		t.Fatalf("generation.Close() error = %v", err)
	}
	hits, err := projection.SearchVector(ctx, sessionmemory.RecallRequest{Scope: validVecgoScope(), Query: "memory", Limit: 2}, []float32{1, 0})
	if err != nil {
		t.Fatalf("SearchVector() error = %v", err)
	}
	if len(hits) != 1 || hits[0].RevisionID != stateTestRevisionOneID || hits[0].RevisionID == "1" {
		t.Fatalf("SearchVector() = %#v, want canonical revision metadata and no numeric backend id", hits)
	}
	if err := projection.Close(); err != nil {
		t.Fatalf("projection.Close() error = %v", err)
	}

	reopened, err := OpenVecgoRecallProjection(ctx, root, config)
	if err != nil {
		t.Fatalf("OpenVecgoRecallProjection(reopen) error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	reopenedHits, err := reopened.SearchVector(ctx, sessionmemory.RecallRequest{Scope: validVecgoScope(), Query: "memory", Limit: 2}, []float32{1, 0})
	if err != nil {
		t.Fatalf("SearchVector(reopen) error = %v", err)
	}
	if len(reopenedHits) != 1 || reopenedHits[0].RevisionID != stateTestRevisionOneID {
		t.Fatalf("SearchVector(reopen) = %#v, want committed canonical revision", reopenedHits)
	}
}

func TestVecgoDirtyGenerationCannotActivate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	projection, err := OpenVecgoRecallProjection(ctx, t.TempDir(), VecgoRecallProjectionConfig{Enabled: true, ModelVersion: "embed-v1", Dimension: 2, Metric: "cosine"})
	if err != nil {
		t.Fatalf("OpenVecgoRecallProjection() error = %v", err)
	}
	t.Cleanup(func() { _ = projection.Close() })
	generation, err := projection.NewGeneration(ctx, "dirty-generation")
	if err != nil {
		t.Fatalf("NewGeneration() error = %v", err)
	}
	t.Cleanup(func() { _ = generation.Close() })
	if err := generation.Index(ctx, validVecgoDocument(stateTestRevisionOneID, "item-1", []float32{1, 0})); err != nil {
		t.Fatalf("Index() error = %v", err)
	}
	if err := projection.ActivateGeneration(ctx, "dirty-generation"); err == nil {
		t.Fatal("ActivateGeneration() succeeded for an uncommitted dirty generation")
	}
}

func TestOptionalVecgoRecallProjectionDisabledDoesNotCreateIndex(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projection, err := OpenOptionalVecgoRecallProjection(context.Background(), root, DefaultVecgoRecallProjectionConfig())
	if err != nil {
		t.Fatalf("OpenOptionalVecgoRecallProjection(disabled) error = %v", err)
	}
	if projection != nil {
		t.Fatal("OpenOptionalVecgoRecallProjection(disabled) returned an adapter")
	}
}

func validVecgoScope() sessionmemory.Scope {
	return sessionmemory.Scope{Key: "telegram:vecgo", Kind: sessionmemory.ScopeKindPersonal}
}

func validVecgoDocument(revisionID, itemID string, embedding []float32) sessionmemory.VectorProjectionDocument {
	now := time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)
	return sessionmemory.VectorProjectionDocument{
		RecallProjectionDocument: sessionmemory.RecallProjectionDocument{
			Scope: validVecgoScope(), ItemID: itemID, RevisionID: revisionID, Revision: 1,
			Kind: sessionmemory.MemoryKindState, Text: "vector memory", CreatedAt: now,
			Temporal: sessionmemory.Temporal{ObservedAt: now}, Sensitivity: sessionmemory.SensitivityStandard,
			Retention: sessionmemory.RetentionClassStandard, SourceIDs: []string{"source-1"},
			SessionIDs: []string{"session-1"}, ScopeChangeSeq: 4,
		},
		Embedding: embedding, EmbeddingModel: "embed-v1",
	}
}
