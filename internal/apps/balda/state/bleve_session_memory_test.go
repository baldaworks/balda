package state

import (
	"context"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestBleveRecallProjectionIndexesRussianAndEnglishWithGenerationSwitch(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	projection, err := NewBleveRecallProjection(root)
	if err != nil {
		t.Fatalf("NewBleveRecallProjection() error = %v", err)
	}
	defer func() { _ = projection.Close() }()
	scope := sessionmemory.Scope{Key: "telegram:1:0", Kind: sessionmemory.ScopeKindPersonal}
	now := time.Date(2026, time.August, 6, 9, 0, 0, 0, time.UTC)
	first := recallProjectionDocument(scope, "item-1", stateTestRevisionOneID, "русское решение", now, 1)
	second := recallProjectionDocument(scope, "item-2", "revision-2", "english deployment decision", now.Add(time.Minute), 2)

	generation, err := projection.NewGeneration("generation-1")
	if err != nil {
		t.Fatalf("NewGeneration() error = %v", err)
	}
	if err := generation.Index(context.Background(), first); err != nil {
		t.Fatalf("Index(first) error = %v", err)
	}
	if err := generation.Index(context.Background(), second); err != nil {
		t.Fatalf("Index(second) error = %v", err)
	}
	if err := generation.Commit(context.Background()); err != nil {
		t.Fatalf("Commit() error = %v", err)
	}
	if err := generation.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := projection.ActivateGeneration(context.Background(), "generation-1"); err != nil {
		t.Fatalf("ActivateGeneration() error = %v", err)
	}

	category := sessionmemory.AtomCategoryDecision
	russianHits, err := projection.SearchRecall(context.Background(), sessionmemory.RecallRequest{
		Scope: scope, Query: "решение", Limit: 10, Category: &category,
	})
	if err != nil {
		t.Fatalf("Russian SearchRecall() error = %v", err)
	}
	if len(russianHits) != 1 || russianHits[0].RevisionID != stateTestRevisionOneID {
		t.Fatalf("Russian SearchRecall() = %#v", russianHits)
	}
	englishHits, err := projection.SearchRecall(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "deployment", Limit: 10})
	if err != nil {
		t.Fatalf("English SearchRecall() error = %v", err)
	}
	if len(englishHits) != 1 || englishHits[0].RevisionID != "revision-2" {
		t.Fatalf("English SearchRecall() = %#v", englishHits)
	}

	generation, err = projection.NewGeneration("generation-2")
	if err != nil {
		t.Fatalf("NewGeneration(generation-2) error = %v", err)
	}
	third := recallProjectionDocument(scope, "item-3", "revision-3", "new canonical choice", now.Add(2*time.Minute), 3)
	if err := generation.Index(context.Background(), third); err != nil {
		t.Fatalf("Index(third) error = %v", err)
	}
	if err := generation.Commit(context.Background()); err != nil {
		t.Fatalf("Commit(generation-2) error = %v", err)
	}
	if err := generation.Close(); err != nil {
		t.Fatalf("Close(generation-2) error = %v", err)
	}
	if err := projection.ActivateGeneration(context.Background(), "generation-2"); err != nil {
		t.Fatalf("ActivateGeneration(generation-2) error = %v", err)
	}
	oldHits, err := projection.SearchRecall(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "deployment", Limit: 10})
	if err != nil {
		t.Fatalf("SearchRecall(after switch) error = %v", err)
	}
	if len(oldHits) != 0 {
		t.Fatalf("SearchRecall(after switch) = %#v, want old generation hidden", oldHits)
	}
	newHits, err := projection.SearchRecall(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "canonical", Limit: 10})
	if err != nil || len(newHits) != 1 || newHits[0].RevisionID != "revision-3" {
		t.Fatalf("SearchRecall(new generation) = %#v, error %v", newHits, err)
	}

	if err := projection.Close(); err != nil {
		t.Fatalf("Close(projection) error = %v", err)
	}
	reopened, err := NewBleveRecallProjection(root)
	if err != nil {
		t.Fatalf("reopen projection error = %v", err)
	}
	defer func() { _ = reopened.Close() }()
	reopenedHits, err := reopened.SearchRecall(context.Background(), sessionmemory.RecallRequest{Scope: scope, Query: "canonical", Limit: 10})
	if err != nil || len(reopenedHits) != 1 || reopenedHits[0].RevisionID != "revision-3" {
		t.Fatalf("SearchRecall(reopened) = %#v, error %v", reopenedHits, err)
	}
}

func recallProjectionDocument(scope sessionmemory.Scope, itemID, revisionID, text string, createdAt time.Time, changeSeq uint64) sessionmemory.RecallProjectionDocument {
	category := sessionmemory.AtomCategoryDecision
	return sessionmemory.RecallProjectionDocument{
		Scope: scope, ItemID: itemID, RevisionID: revisionID, Revision: 1,
		Kind: sessionmemory.MemoryKindState, Category: &category, MemoryKey: sessionmemory.MemoryKey(itemID + "-key"),
		Text: text, CreatedAt: createdAt, Temporal: sessionmemory.Temporal{ObservedAt: createdAt},
		Sensitivity: sessionmemory.SensitivityStandard, Retention: sessionmemory.RetentionClassStandard,
		SourceIDs: []string{"source-1"}, SessionIDs: []string{"session-1"}, ScopeChangeSeq: changeSeq,
	}
}
