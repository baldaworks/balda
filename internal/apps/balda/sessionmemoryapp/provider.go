package sessionmemoryapp

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/sessionmemory"
)

// DisabledProvider is the stable no-op provider used when session memory is
// disabled. Keeping a concrete implementation in the graph lets Fx compose a
// disabled application without constructing an HTTP client or a consumer.
type DisabledProvider struct{}

func (DisabledProvider) SyncTurn(context.Context, sessionmemory.Turn) error {
	return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) OnSessionBoundary(context.Context, sessionmemory.Boundary) error {
	return sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Search(context.Context, sessionmemory.SearchRequest) (sessionmemory.SearchResponse, error) {
	return sessionmemory.SearchResponse{}, sessionmemory.PermanentError(sessionmemory.CodeDisabled, "session-memory provider is disabled", nil)
}

func (DisabledProvider) Close(context.Context) error { return nil }

var _ sessionmemory.Provider = DisabledProvider{}
