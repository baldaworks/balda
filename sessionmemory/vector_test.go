package sessionmemory

import (
	"testing"
	"time"
)

var fixedVectorTime = time.Date(2026, time.August, 6, 10, 0, 0, 0, time.UTC)

func hasErrorCode(err error, want ErrorCode) bool {
	code, _, ok := ClassifyError(err)
	return ok && code == want
}

func TestFuseRecallProjectionHitsUsesBoundedRRFAndExactScope(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:rrf", Kind: ScopeKindPersonal}
	lexical := []RecallProjectionHit{
		{Scope: scope, ItemID: "item-a", RevisionID: "revision-a", Revision: 1, Score: 4},
		{Scope: scope, ItemID: "item-b", RevisionID: "revision-b", Revision: 1, Score: 3},
	}
	vector := []RecallProjectionHit{
		{Scope: scope, ItemID: "item-b", RevisionID: "revision-b", Revision: 1, Score: 0.9, ScopeChangeSeq: 2},
		{Scope: scope, ItemID: "item-c", RevisionID: "revision-c", Revision: 1, Score: 0.8},
	}
	got, err := FuseRecallProjectionHits(scope, lexical, vector, 2)
	if err != nil {
		t.Fatalf("FuseRecallProjectionHits() error = %v", err)
	}
	if len(got) != 2 || got[0].RevisionID != "revision-b" || got[1].RevisionID != "revision-a" {
		t.Fatalf("FuseRecallProjectionHits() = %#v, want merged reciprocal-rank order", got)
	}
	if got[0].ScopeChangeSeq != 2 || got[0].Score <= got[1].Score {
		t.Fatalf("merged vector hit = %#v, want highest score and latest watermark", got[0])
	}
}

func TestFuseRecallProjectionHitsRejectsForeignScope(t *testing.T) {
	t.Parallel()
	scope := Scope{Key: "telegram:rrf", Kind: ScopeKindPersonal}
	foreign := Scope{Key: "telegram:other", Kind: ScopeKindPersonal}
	if _, err := FuseRecallProjectionHits(scope, []RecallProjectionHit{{Scope: foreign, RevisionID: "revision-1"}}, nil, 1); !hasErrorCode(err, CodeScopeViolation) {
		t.Fatalf("foreign scope error = %v, want scope violation", err)
	}
}

func TestVectorProjectionDocumentRejectsModelAndDimensionDrift(t *testing.T) {
	t.Parallel()
	document := validVectorProjectionDocument()
	if err := document.Validate("embed-v1", 2); err != nil {
		t.Fatalf("valid vector document rejected: %v", err)
	}
	if err := document.Validate("embed-v2", 2); !hasErrorCode(err, CodeConflict) {
		t.Fatalf("model drift error = %v, want conflict", err)
	}
	if err := document.Validate("embed-v1", 3); !hasErrorCode(err, CodeConflict) {
		t.Fatalf("dimension drift error = %v, want conflict", err)
	}
}

func validVectorProjectionDocument() VectorProjectionDocument {
	scope := Scope{Key: "telegram:vector", Kind: ScopeKindPersonal}
	return VectorProjectionDocument{
		RecallProjectionDocument: RecallProjectionDocument{
			Scope: scope, ItemID: "item-1", RevisionID: "revision-1", Revision: 1,
			Kind: MemoryKindState, Text: "vector memory", CreatedAt: fixedVectorTime,
			Temporal: Temporal{ObservedAt: fixedVectorTime}, Sensitivity: SensitivityStandard,
			Retention: RetentionClassStandard, SourceIDs: []string{"source-1"},
			SessionIDs: []string{"session-1"}, ScopeChangeSeq: 1,
		},
		Embedding: []float32{1, 0}, EmbeddingModel: "embed-v1",
	}
}
