// Package hookbackofficeapp binds the shared hook owner to Backoffice's port.
package hookbackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
)

// Operations adapts guarded management without owning browser or runtime policy.
type Operations struct{ management *hooks.Management }

// New binds the application's single hook management owner.
func New(management *hooks.Management) *Operations { return &Operations{management: management} }
func (o *Operations) Inventory(ctx context.Context, a hookcmd.Authority) (hookcmd.Record, error) {
	return o.management.Inventory(ctx, a)
}
func (o *Operations) Save(ctx context.Context, request hookcmd.Save) error {
	return o.management.Save(ctx, request)
}
func (o *Operations) Delete(ctx context.Context, request hookcmd.Delete) error {
	return o.management.Delete(ctx, request)
}
