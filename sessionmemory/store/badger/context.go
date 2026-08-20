package badger

import (
	"context"

	"github.com/baldaworks/balda/sessionmemory"
)

func sessionMemoryContextError(ctx context.Context) error {
	if ctx == nil {
		return sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "context is required", nil)
	}
	if err := ctx.Err(); err != nil {
		return sessionmemory.RetryableError(sessionmemory.CodeTimeout, "session-memory operation canceled", err)
	}
	return nil
}
