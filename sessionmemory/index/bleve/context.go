package bleve

import (
	"context"

	"github.com/normahq/balda/sessionmemory"
)

func sessionMemoryContextError(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "context is required", nil)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
