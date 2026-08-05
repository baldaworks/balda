package sessionmemory

import (
	"context"
	"time"
)

const maxCanonicalForgetBatch = 512

// CanonicalSourceForgetStore is the narrow persistence port required for one
// resumable logical-forget batch. Implementations persist records and bounded
// provenance scans only; they do not choose cascade order or retry policy.
type CanonicalSourceForgetStore interface {
	DenySource(ctx context.Context, scope Scope, sourceID string, deniedAt time.Time) error
	SourceRevisionBatch(ctx context.Context, scope Scope, sourceID, afterRevisionID string, limit uint32) ([]string, string, error)
	DenyRevision(ctx context.Context, scope Scope, revisionID string, deniedAt time.Time) error
}

// CanonicalSourceForgetBatchRequest resumes one source's direct provenance
// traversal after AfterRevisionID. An empty cursor starts a new traversal.
type CanonicalSourceForgetBatchRequest struct {
	Scope           Scope     `json:"scope"`
	SourceID        string    `json:"source_id"`
	AfterRevisionID string    `json:"after_revision_id,omitempty"`
	Limit           uint32    `json:"limit"`
	DeniedAt        time.Time `json:"denied_at"`
}

// CanonicalSourceForgetBatchResult reports only immutable identities so it is
// safe to persist as a checkpoint without retaining removed payload content.
type CanonicalSourceForgetBatchResult struct {
	RevisionIDs []string `json:"revision_ids,omitempty"`
	NextCursor  string   `json:"next_cursor,omitempty"`
}

// Validate verifies that a bounded source-forget batch cannot cross scopes or
// use an unbounded cursor request.
func (r CanonicalSourceForgetBatchRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(r.SourceID) || (r.AfterRevisionID != "" && !isCanonicalID(r.AfterRevisionID)) || r.Limit == 0 || r.Limit > maxCanonicalForgetBatch || r.DeniedAt.IsZero() {
		return invalidDerived("canonical source forget batch request is invalid")
	}
	return nil
}

// DenyCanonicalSourceBatch performs one bounded logical-forget step. It marks
// the source denied before any provenance scan, so recall remains fail-closed
// if a later batch or revision write is interrupted. Higher-level workers own
// repeated invocation, checkpoint persistence, physical scrubbing, and retry.
func DenyCanonicalSourceBatch(ctx context.Context, store CanonicalSourceForgetStore, request CanonicalSourceForgetBatchRequest) (CanonicalSourceForgetBatchResult, error) {
	if ctx == nil {
		return CanonicalSourceForgetBatchResult{}, PermanentError(CodeInvalidDerived, "canonical source forget context is required", nil)
	}
	if store == nil {
		return CanonicalSourceForgetBatchResult{}, PermanentError(CodeStoreFailure, "canonical source forget store is required", nil)
	}
	if err := request.Validate(); err != nil {
		return CanonicalSourceForgetBatchResult{}, err
	}
	if err := store.DenySource(ctx, request.Scope, request.SourceID, request.DeniedAt); err != nil {
		return CanonicalSourceForgetBatchResult{}, err
	}
	revisionIDs, nextCursor, err := store.SourceRevisionBatch(ctx, request.Scope, request.SourceID, request.AfterRevisionID, request.Limit)
	if err != nil {
		return CanonicalSourceForgetBatchResult{}, err
	}
	for _, revisionID := range revisionIDs {
		if !isCanonicalID(revisionID) {
			return CanonicalSourceForgetBatchResult{}, PermanentError(CodeStoreFailure, "canonical source forget store returned an invalid revision identity", nil)
		}
		if err := store.DenyRevision(ctx, request.Scope, revisionID, request.DeniedAt); err != nil {
			return CanonicalSourceForgetBatchResult{}, err
		}
	}
	if nextCursor != "" && !isCanonicalID(nextCursor) {
		return CanonicalSourceForgetBatchResult{}, PermanentError(CodeStoreFailure, "canonical source forget store returned an invalid cursor", nil)
	}
	return CanonicalSourceForgetBatchResult{RevisionIDs: append([]string(nil), revisionIDs...), NextCursor: nextCursor}, nil
}
