// Package aliasbackofficeapp adapts host-managed destination policy to Backoffice.
package aliasbackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/aliases"
)

// Operations delegates guarded management to the alias owner.
type Operations struct{ service *aliases.Service }

func New(service *aliases.Service) *Operations { return &Operations{service: service} }

func (o *Operations) List(ctx context.Context, authority aliascmd.Authority) ([]aliascmd.Record, error) {
	return o.service.List(ctx, authority)
}

func (o *Operations) Get(ctx context.Context, name string, authority aliascmd.Authority) (aliascmd.Record, error) {
	return o.service.Get(ctx, name, authority)
}

func (o *Operations) Create(ctx context.Context, request aliascmd.Create) (aliascmd.Record, error) {
	return o.service.Create(ctx, request)
}

func (o *Operations) Retarget(ctx context.Context, request aliascmd.Retarget) (aliascmd.Record, error) {
	return o.service.Retarget(ctx, request)
}

func (o *Operations) Delete(ctx context.Context, request aliascmd.Delete) error {
	return o.service.Delete(ctx, request)
}
