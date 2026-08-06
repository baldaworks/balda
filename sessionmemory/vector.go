package sessionmemory

import (
	"context"
	"math"
	"sort"
)

const (
	// VectorProjectionSchemaV1 identifies the storage-neutral vector projection
	// contract. Vector data is disposable; canonical revisions remain the
	// source of truth for every returned reference.
	VectorProjectionSchemaV1 = "session-memory-vector/v1"
	// MaxVectorDimensions bounds an embedding at the application boundary.
	MaxVectorDimensions = 4096
	// RecallRRFConstant is the standard reciprocal-rank-fusion smoothing value.
	RecallRRFConstant = 60
)

// VectorProjectionDocument is the rebuildable input to an optional semantic
// projection. The projection stores only the embedding and bounded metadata;
// RecallService hydrates the canonical revision before exposing text.
type VectorProjectionDocument struct {
	RecallProjectionDocument
	Embedding      []float32
	EmbeddingModel string
}

// Validate verifies one vector document against a configured embedding
// contract. The model and dimension are versioned so an incompatible index is
// rebuilt instead of being silently queried.
func (d VectorProjectionDocument) Validate(model string, dimension int) error {
	if err := d.RecallProjectionDocument.Validate(); err != nil {
		return err
	}
	if model == "" || dimension < 1 || dimension > MaxVectorDimensions {
		return invalidDerived("vector projection configuration is invalid")
	}
	if d.EmbeddingModel != model || len(d.Embedding) != dimension {
		return PermanentError(CodeConflict, "vector embedding model or dimension does not match projection", nil)
	}
	for _, value := range d.Embedding {
		if math.IsNaN(float64(value)) || math.IsInf(float64(value), 0) {
			return invalidDerived("vector embedding contains a non-finite value")
		}
	}
	return nil
}

// VectorRecallProjection is the optional semantic-candidate port. It is
// intentionally separate from RecallProjection because producing a query
// embedding is an application concern and Bleve-only recall must remain
// complete when this port is disabled.
type VectorRecallProjection interface {
	SearchVector(ctx context.Context, request RecallRequest, embedding []float32) ([]RecallProjectionHit, error)
}

// FuseRecallProjectionHits combines two exact-scope candidate lists with
// reciprocal-rank fusion. Canonical hydration still owns lifecycle,
// forgetting, sensitivity, and provenance checks after this operation.
func FuseRecallProjectionHits(scope Scope, lexical, vector []RecallProjectionHit, limit int) ([]RecallProjectionHit, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	if limit < 1 || limit > MaxRecallCandidates {
		return nil, PermanentError(CodeInvalidQuery, "fusion limit is outside the allowed range", nil)
	}
	type fused struct {
		hit   RecallProjectionHit
		score float64
	}
	byRevision := make(map[string]fused, len(lexical)+len(vector))
	add := func(values []RecallProjectionHit) error {
		seen := make(map[string]struct{}, len(values))
		for rank, hit := range values {
			if hit.Scope != scope {
				return PermanentError(CodeScopeViolation, "recall fusion received a foreign scope", nil)
			}
			if hit.RevisionID == "" {
				return PermanentError(CodeInvalidDerived, "recall fusion received an empty revision", nil)
			}
			if _, exists := seen[hit.RevisionID]; exists {
				continue
			}
			seen[hit.RevisionID] = struct{}{}
			contribution := 1 / float64(RecallRRFConstant+rank+1)
			current, exists := byRevision[hit.RevisionID]
			if !exists {
				byRevision[hit.RevisionID] = fused{hit: hit, score: contribution}
				continue
			}
			current.score += contribution
			if hit.Revision > current.hit.Revision || current.hit.ItemID == "" {
				current.hit.ItemID = hit.ItemID
				current.hit.Revision = hit.Revision
			}
			if hit.ScopeChangeSeq > current.hit.ScopeChangeSeq {
				current.hit.ScopeChangeSeq = hit.ScopeChangeSeq
			}
			if current.hit.Score == 0 && hit.Score != 0 {
				current.hit.Score = hit.Score
			}
			byRevision[hit.RevisionID] = current
		}
		return nil
	}
	if err := add(lexical); err != nil {
		return nil, err
	}
	if err := add(vector); err != nil {
		return nil, err
	}
	results := make([]RecallProjectionHit, 0, len(byRevision))
	for _, value := range byRevision {
		value.hit.Score = value.score
		results = append(results, value.hit)
	}
	sort.SliceStable(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].RevisionID < results[j].RevisionID
	})
	if len(results) > limit {
		results = results[:limit]
	}
	return results, nil
}
