// Package sessionmemorytest provides deterministic test support for Store and
// model-port implementations. Its in-memory Store is not a production backend.
package sessionmemorytest

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/normahq/balda/sessionmemory"
)

// Store is a deterministic, concurrency-safe in-memory implementation used by
// the reusable contract suite and consumer tests.
type Store struct {
	mu     sync.RWMutex
	scopes map[sessionmemory.Scope]*scopeState
}

type scopeState struct {
	snapshot   sessionmemory.ScopeSnapshot
	operations map[string]operationRecord
	forgets    map[string]forgetRecord
}

type operationRecord struct {
	request sessionmemory.CommitRequest
	outcome sessionmemory.OperationOutcome
}

type forgetRecord struct {
	sourceRequest *sessionmemory.ForgetSourceRequest
	scopeRequest  *sessionmemory.ForgetScopeRequest
	outcome       sessionmemory.ForgetOutcome
}

// NewStore returns an empty deterministic test Store.
func NewStore() *Store {
	return &Store{scopes: make(map[sessionmemory.Scope]*scopeState)}
}

// LookupOperation implements sessionmemory.Store.
func (s *Store) LookupOperation(
	ctx context.Context,
	lookup sessionmemory.OperationLookup,
) (sessionmemory.OperationLookupResult, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	if err := lookup.Validate(); err != nil {
		return sessionmemory.OperationLookupResult{}, err
	}
	return readScope(s, lookup.Scope, func(state *scopeState) (sessionmemory.OperationLookupResult, error) {
		record, ok := state.operations[lookup.OperationID]
		if !ok {
			return sessionmemory.OperationLookupResult{}, nil
		}
		if record.request.Stage != lookup.Stage || record.request.Scope != lookup.Scope {
			return sessionmemory.OperationLookupResult{}, conflict("operation identity was reused")
		}
		return clone(sessionmemory.OperationLookupResult{Found: true, Outcome: record.outcome})
	})
}

// LookupForget implements sessionmemory.Store.
func (s *Store) LookupForget(
	ctx context.Context,
	lookup sessionmemory.ForgetLookup,
) (sessionmemory.ForgetLookupResult, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.ForgetLookupResult{}, err
	}
	if err := lookup.Validate(); err != nil {
		return sessionmemory.ForgetLookupResult{}, err
	}
	return readScope(s, lookup.Scope, func(state *scopeState) (sessionmemory.ForgetLookupResult, error) {
		record, ok := state.forgets[lookup.OperationID]
		if !ok {
			return sessionmemory.ForgetLookupResult{}, nil
		}
		if record.outcome.Kind != lookup.Kind || record.outcome.Scope != lookup.Scope {
			return sessionmemory.ForgetLookupResult{}, conflict("forget identity was reused")
		}
		return clone(sessionmemory.ForgetLookupResult{Found: true, Outcome: record.outcome})
	})
}

// LoadScope implements sessionmemory.Store. Missing scopes are returned as an
// empty version-zero snapshot with the requested exact locator identity.
func (s *Store) LoadScope(ctx context.Context, scope sessionmemory.Scope) (sessionmemory.ScopeSnapshot, error) {
	if err := contextError(ctx); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	if err := scope.Validate(); err != nil {
		return sessionmemory.ScopeSnapshot{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[scope]
	if state == nil {
		return sessionmemory.ScopeSnapshot{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Scope:         scope,
		}, nil
	}
	return clone(state.snapshot)
}

func (s *Store) stateLocked(scope sessionmemory.Scope) *scopeState {
	state := s.scopes[scope]
	if state != nil {
		return state
	}
	state = &scopeState{
		snapshot: sessionmemory.ScopeSnapshot{
			SchemaVersion: sessionmemory.DerivedSchemaVersionV1,
			Scope:         scope,
		},
		operations: make(map[string]operationRecord),
		forgets:    make(map[string]forgetRecord),
	}
	s.scopes[scope] = state
	return state
}

func readScope[T any](s *Store, scope sessionmemory.Scope, read func(*scopeState) (T, error)) (T, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	state := s.scopes[scope]
	if state == nil {
		var zero T
		return zero, nil
	}
	return read(state)
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "test Store operation canceled", nil)
	}
	return nil
}

func conflict(message string) error {
	return sessionmemory.PermanentError(sessionmemory.CodeConflict, message, nil)
}

func clone[T any](value T) (T, error) {
	var cloned T
	data, err := json.Marshal(value)
	if err != nil {
		return cloned, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "test Store clone failed", nil)
	}
	if err := json.Unmarshal(data, &cloned); err != nil {
		return cloned, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "test Store clone failed", nil)
	}
	return cloned, nil
}

func revisionRef(meta sessionmemory.RevisionMeta) sessionmemory.RevisionRef {
	return sessionmemory.RevisionRef{ItemID: meta.ItemID, RevisionID: meta.RevisionID}
}
