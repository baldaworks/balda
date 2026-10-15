package state

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
)

// AliasStore persists managed names and validates browser authority with writes.
type AliasStore interface {
	CheckAuthority(ctx context.Context, authority aliascmd.Authority) error
	Get(ctx context.Context, name string) (aliascmd.Record, bool, error)
	List(ctx context.Context) ([]aliascmd.Record, error)
	Save(ctx context.Context, mutation aliascmd.Mutation) error
}
