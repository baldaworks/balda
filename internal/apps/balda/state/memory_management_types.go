package state

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
)

// MemoryManagementStore atomically fences browser mutations of global memory.
type MemoryManagementStore interface {
	CheckAuthority(ctx context.Context, authority memorycmd.Authority) error
	Commit(ctx context.Context, mutation memorycmd.Mutation) error
}
