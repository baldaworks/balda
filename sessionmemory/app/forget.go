package app

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

// ForgetService is the portable mutating application boundary.  The
// canonical store adapter owns the atomic denial/cascade transaction; this
// service keeps the capability explicit and prevents transport packages from
// reaching into concrete storage.
type ForgetService struct {
	operations Forgetter
	projection ProjectionSyncer
}

// NewForgetService constructs a forget capability over a canonical operation
// port.  A host may use this adapter while it migrates an existing canonical
// implementation into the public storage package.
func NewForgetService(operations Forgetter) (*ForgetService, error) {
	return NewForgetServiceWithProjection(operations, nil)
}

// NewForgetServiceWithProjection composes canonical logical denial with the
// rebuildable projection side effect. Canonical denial remains authoritative
// when a later projection retry is required.
func NewForgetServiceWithProjection(operations Forgetter, projection ProjectionSyncer) (*ForgetService, error) {
	if operations == nil {
		return nil, sessionmemory.PermanentError(sessionmemory.CodeStoreFailure, "forget operations are required", nil)
	}
	return &ForgetService{operations: operations, projection: projection}, nil
}

// ForgetSource delegates a source denial without widening its scope.
func (s *ForgetService) ForgetSource(ctx context.Context, command sessionmemory.ForgetSourceCommand) (sessionmemory.ForgetOutcome, error) {
	if s == nil || s.operations == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "forget service is unavailable", nil)
	}
	if err := command.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	outcome, err := s.operations.ForgetSource(ctx, command)
	if err != nil || s.projection == nil {
		return outcome, err
	}
	if projectErr := s.projection.Sync(ctx, command.Source.Scope); projectErr != nil {
		return outcome, projectErr
	}
	return outcome, nil
}

// ForgetScope delegates an exact-scope denial without exposing a remote
// mutation surface.
func (s *ForgetService) ForgetScope(ctx context.Context, command sessionmemory.ForgetScopeCommand) (sessionmemory.ForgetOutcome, error) {
	if s == nil || s.operations == nil {
		return sessionmemory.ForgetOutcome{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "forget service is unavailable", nil)
	}
	if err := command.Validate(); err != nil {
		return sessionmemory.ForgetOutcome{}, err
	}
	outcome, err := s.operations.ForgetScope(ctx, command)
	if err != nil || s.projection == nil {
		return outcome, err
	}
	if projectErr := s.projection.Sync(ctx, command.Scope); projectErr != nil {
		return outcome, projectErr
	}
	return outcome, nil
}

var _ Forgetter = (*ForgetService)(nil)
