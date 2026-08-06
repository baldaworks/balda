package sessionmemoryapp

import (
	"context"
	"strings"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// RecallService owns canonical hydration and fail-closed validation around a
// disposable lexical or hybrid projection. The projection is never trusted
// with scope, lifecycle, temporal, sensitivity, or payload truth.
type RecallService struct {
	canonical  sessionmemory.RecallCanonicalReader
	projection sessionmemory.RecallProjection
	vector     sessionmemory.VectorRecallProjection
	now        func() time.Time
}

// NewRecallService constructs the bounded canonical recall use case. A nil
// projection is valid and selects the bounded canonical-tail fallback.
func NewRecallService(canonical sessionmemory.RecallCanonicalReader, projection sessionmemory.RecallProjection) (*RecallService, error) {
	return NewHybridRecallService(canonical, projection, nil)
}

// NewHybridRecallService wires optional semantic candidates beside the
// mandatory lexical projection. A nil vector projection keeps the complete
// Bleve-only path and requires no data migration when semantic recall is off.
func NewHybridRecallService(canonical sessionmemory.RecallCanonicalReader, projection sessionmemory.RecallProjection, vector sessionmemory.VectorRecallProjection) (*RecallService, error) {
	if canonical == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "recall canonical reader is required", nil)
	}
	return &RecallService{canonical: canonical, projection: projection, vector: vector, now: time.Now}, nil
}

// Search executes one exact-scope recall. Candidate IDs are always hydrated
// from canonical storage before text or evidence is returned.
func (s *RecallService) Search(ctx context.Context, request sessionmemory.RecallRequest) (sessionmemory.RecallResponse, error) {
	if s == nil || s.canonical == nil {
		return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "recall service is unavailable", nil)
	}
	if ctx == nil {
		return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidQuery, "recall context is required", nil)
	}
	normalized, err := sessionmemory.NormalizeRecallRequest(request)
	if err != nil {
		return sessionmemory.RecallResponse{}, err
	}
	now := time.Now().UTC()
	if s.now != nil {
		now = s.now().UTC()
	}
	currentSeq, err := s.canonical.CurrentScopeChangeSeq(ctx, normalized.Scope)
	if err != nil {
		return sessionmemory.RecallResponse{}, err
	}
	if currentSeq < normalized.MinScopeChangeSeq {
		return sessionmemory.RecallResponse{}, sessionmemory.RetryableError(sessionmemory.CodeConflict, "recall consistency watermark has not caught up", nil)
	}

	hits := make([]sessionmemory.RecallProjectionHit, 0, normalized.Limit)
	if s.projection != nil {
		hits, err = s.projection.SearchRecall(ctx, normalized)
		if err != nil {
			return sessionmemory.RecallResponse{}, err
		}
		if len(hits) > sessionmemory.MaxRecallCandidates {
			return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeLimitExceeded, "recall projection candidate bound exceeded", nil)
		}
	}
	if s.vector != nil && len(normalized.Embedding) > 0 {
		vectorHits, vectorErr := s.vector.SearchVector(ctx, normalized, normalized.Embedding)
		if vectorErr != nil {
			return sessionmemory.RecallResponse{}, vectorErr
		}
		hits, err = sessionmemory.FuseRecallProjectionHits(normalized.Scope, hits, vectorHits, sessionmemory.MaxRecallCandidates)
		if err != nil {
			return sessionmemory.RecallResponse{}, err
		}
	}

	// A missing/empty generation is a safe diagnostic fallback, not a full
	// scope scan. The canonical reader owns the bounded tail implementation.
	if len(hits) == 0 {
		records, tailErr := s.canonical.SearchRecallTail(ctx, normalized, recallTailLimit(normalized.Limit))
		if tailErr != nil {
			return sessionmemory.RecallResponse{}, tailErr
		}
		return s.responseFromRecords(normalized, records, currentSeq, now)
	}

	ids := make([]string, 0, len(hits))
	byID := make(map[string]sessionmemory.RecallProjectionHit, len(hits))
	for _, hit := range hits {
		if hit.Scope != normalized.Scope {
			return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeScopeViolation, "recall projection returned a foreign scope", nil)
		}
		if hit.RevisionID == "" {
			return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "recall projection returned an empty revision", nil)
		}
		if _, exists := byID[hit.RevisionID]; exists {
			continue
		}
		byID[hit.RevisionID] = hit
		ids = append(ids, hit.RevisionID)
	}
	if len(ids) == 0 {
		return emptyRecallResponse(normalized.Scope, currentSeq), nil
	}
	records, err := s.canonical.LoadRecallRecords(ctx, normalized.Scope, ids)
	if err != nil {
		return sessionmemory.RecallResponse{}, err
	}
	return s.responseFromProjection(normalized, records, byID, currentSeq, now)
}

func (s *RecallService) responseFromProjection(request sessionmemory.RecallRequest, records []sessionmemory.RecallRecord, hits map[string]sessionmemory.RecallProjectionHit, currentSeq uint64, now time.Time) (sessionmemory.RecallResponse, error) {
	results := make([]sessionmemory.RecallReference, 0, request.Limit)
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		hit, exists := hits[record.RevisionID]
		if !exists {
			continue
		}
		if _, exists := seen[record.RevisionID]; exists {
			continue
		}
		if err := record.Validate(request, now); err != nil {
			if skipStaleRecallError(err) {
				continue
			}
			return sessionmemory.RecallResponse{}, err
		}
		if hit.ScopeChangeSeq != 0 && hit.ScopeChangeSeq > currentSeq {
			return sessionmemory.RecallResponse{}, sessionmemory.PermanentError(sessionmemory.CodeConflict, "recall projection is ahead of canonical state", nil)
		}
		reference := recallReference(record, sessionmemory.RankRecall(hit.Score, record.CreatedAt, now))
		results = append(results, reference)
		seen[record.RevisionID] = struct{}{}
		if len(results) == request.Limit {
			break
		}
	}
	sessionmemory.SortRecallReferences(results)
	return sessionmemory.RecallResponse{
		SchemaVersion:  sessionmemory.RecallSchemaVersionV1,
		Trust:          sessionmemory.ReferenceTrustUntrusted,
		Scope:          request.Scope,
		ScopeChangeSeq: currentSeq,
		Results:        results,
	}, nil
}

func (s *RecallService) responseFromRecords(request sessionmemory.RecallRequest, records []sessionmemory.RecallRecord, currentSeq uint64, now time.Time) (sessionmemory.RecallResponse, error) {
	results := make([]sessionmemory.RecallReference, 0, request.Limit)
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if _, exists := seen[record.RevisionID]; exists {
			continue
		}
		if err := record.Validate(request, now); err != nil {
			if skipStaleRecallError(err) {
				continue
			}
			return sessionmemory.RecallResponse{}, err
		}
		tokens := strings.Fields(strings.ToLower(request.Query))
		matched := 0
		lowerText := strings.ToLower(record.Text)
		for _, token := range tokens {
			if strings.Contains(lowerText, token) {
				matched++
			}
		}
		if matched == 0 {
			continue
		}
		score := float64(matched) / float64(len(tokens))
		results = append(results, recallReference(record, sessionmemory.RankRecall(score, record.CreatedAt, now)))
		seen[record.RevisionID] = struct{}{}
		if len(results) == request.Limit {
			break
		}
	}
	sessionmemory.SortRecallReferences(results)
	return sessionmemory.RecallResponse{
		SchemaVersion:  sessionmemory.RecallSchemaVersionV1,
		Trust:          sessionmemory.ReferenceTrustUntrusted,
		Scope:          request.Scope,
		ScopeChangeSeq: currentSeq,
		Results:        results,
	}, nil
}

func recallReference(record sessionmemory.RecallRecord, score sessionmemory.RecallScore) sessionmemory.RecallReference {
	evidence := append([]sessionmemory.EvidenceRef(nil), record.Evidence...)
	if len(evidence) > sessionmemory.MaxRecallEvidence {
		evidence = evidence[:sessionmemory.MaxRecallEvidence]
	}
	category := cloneRecallCategory(record.Category)
	return sessionmemory.RecallReference{
		SchemaVersion:  sessionmemory.RecallSchemaVersionV1,
		Trust:          sessionmemory.ReferenceTrustUntrusted,
		Scope:          record.Scope,
		ItemID:         record.ItemID,
		RevisionID:     record.RevisionID,
		Revision:       record.Revision,
		Kind:           record.Kind,
		State:          record.State,
		Category:       category,
		MemoryKey:      record.MemoryKey,
		Text:           record.Text,
		CreatedAt:      record.CreatedAt,
		Score:          score.Total,
		Explain:        score,
		Evidence:       evidence,
		ScopeChangeSeq: record.ScopeChangeSeq,
	}
}

func cloneRecallCategory(value *sessionmemory.AtomCategory) *sessionmemory.AtomCategory {
	if value == nil {
		return nil
	}
	copyOfValue := *value
	return &copyOfValue
}

func skipStaleRecallError(err error) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	if !ok {
		return false
	}
	switch code {
	case sessionmemory.CodeNotFound, sessionmemory.CodeForgotten:
		return true
	default:
		return false
	}
}

func recallTailLimit(limit int) uint32 {
	if limit < 1 {
		return 1
	}
	want := limit * 4
	if want < limit {
		want = limit
	}
	if want > sessionmemory.MaxRecallCandidates {
		want = sessionmemory.MaxRecallCandidates
	}
	return uint32(want)
}

func emptyRecallResponse(scope sessionmemory.Scope, sequence uint64) sessionmemory.RecallResponse {
	return sessionmemory.RecallResponse{
		SchemaVersion:  sessionmemory.RecallSchemaVersionV1,
		Trust:          sessionmemory.ReferenceTrustUntrusted,
		Scope:          scope,
		ScopeChangeSeq: sequence,
		Results:        []sessionmemory.RecallReference{},
	}
}
