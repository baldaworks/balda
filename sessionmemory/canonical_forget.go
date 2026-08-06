package sessionmemory

import (
	"context"
	"strings"
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

// CanonicalForgetEnumerator is the bounded exact-scope enumeration port used
// by a scope forget. Implementations return deterministic cursors and never
// expose payload bytes.
type CanonicalForgetEnumerator interface {
	ListCanonicalSourceRefs(ctx context.Context, scope Scope, afterSourceID string, limit uint32) ([]SourceRef, string, error)
	ListCanonicalRevisionRefs(ctx context.Context, scope Scope, afterRevisionID string, limit uint32) ([]RevisionRef, string, error)
}

// CanonicalSourceIdentityResolver maps a public source reference to its
// canonical evidence identity. Migration adapters use this to preserve the
// original export identity while native turns use TurnSourceID.
type CanonicalSourceIdentityResolver interface {
	CanonicalSourceID(ctx context.Context, scope Scope, source SourceRef) (string, error)
}

// CanonicalForgetCommitRequest is the content-free durable outcome written
// after logical denial. The store enforces the exact operation fingerprint and
// scope-version CAS, so replay cannot silently change the requested target.
type CanonicalForgetCommitRequest struct {
	Scope                Scope
	OperationID          string
	Kind                 ForgetKind
	Fingerprint          string
	ExpectedScopeVersion uint64
	Sources              []SourceRef
	Revisions            []RevisionRef
	ForgottenAt          time.Time
}

// CanonicalForgetOutcomeStore persists idempotent forget outcomes separately
// from immutable memory payloads. It is optional for portable callers, but a
// production canonical owner implements it so retries return the exact prior
// outcome.
type CanonicalForgetOutcomeStore interface {
	LoadCanonicalForgetOutcome(ctx context.Context, scope Scope, operationID string, kind ForgetKind) (ForgetOutcome, bool, error)
	CommitCanonicalForget(ctx context.Context, request CanonicalForgetCommitRequest) (ForgetOutcome, error)
}

func (r CanonicalForgetCommitRequest) Validate() error {
	if err := r.Scope.Validate(); err != nil {
		return err
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if !isCanonicalID(r.OperationID) || !isCanonicalID(r.Fingerprint) || r.ExpectedScopeVersion == ^uint64(0) || r.ForgottenAt.IsZero() {
		return invalidDerived("canonical forget commit identity or timestamp is invalid")
	}
	for _, source := range r.Sources {
		if err := source.Validate(); err != nil {
			return err
		}
		if source.Scope != r.Scope {
			return PermanentError(CodeScopeViolation, "canonical forget source scope does not match", nil)
		}
	}
	for _, revision := range r.Revisions {
		if err := revision.Validate(); err != nil {
			return err
		}
	}
	return nil
}

// CanonicalForgetOperationID derives a stable exact-scope operation identity
// from the public command. It is deliberately content-free and independent of
// caller timestamps so retries with a different wall clock replay exactly.
func CanonicalForgetOperationID(kind ForgetKind, scope Scope, source SourceRef, requestID string) (string, error) {
	if err := kind.Validate(); err != nil {
		return "", err
	}
	if err := scope.Validate(); err != nil {
		return "", err
	}
	parts := []string{"canonical-forget", string(kind), string(scope.Kind), scope.Key}
	if kind == ForgetKindSource {
		if err := source.Validate(); err != nil {
			return "", err
		}
		if source.Scope != scope {
			return "", PermanentError(CodeScopeViolation, "canonical forget source scope does not match", nil)
		}
		parts = append(parts, source.ExportID, source.SessionID, source.SourceTurnID)
	} else {
		requestID = strings.TrimSpace(requestID)
		if !isCanonicalID(requestID) {
			return "", invalidDerived("canonical scope forget request id is required")
		}
		parts = append(parts, requestID)
	}
	return reconciliationID(parts[0], parts[1:]...), nil
}

// CanonicalForgetFingerprint binds the exact request shape to the operation
// identity without including mutable timestamps.
func CanonicalForgetFingerprint(operationID string, scope Scope, kind ForgetKind, source SourceRef, requestID string) string {
	parts := []string{operationID, string(kind), scope.Key, string(scope.Kind), source.ExportID, source.SessionID, source.SourceTurnID, strings.TrimSpace(requestID)}
	return reconciliationID("canonical-forget-fingerprint", parts...)
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
