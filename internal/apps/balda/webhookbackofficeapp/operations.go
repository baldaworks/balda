// Package webhookbackofficeapp adapts webhook route policy to Backoffice.
package webhookbackofficeapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

// Operations delegates guarded route management to its application owner.
type Operations struct{ service *webhookmanagement.Service }

// New binds the webhook route policy to Backoffice's consuming port.
func New(service *webhookmanagement.Service) *Operations { return &Operations{service: service} }

func (o *Operations) Inventory(ctx context.Context, authority webhookroutecmd.Authority) ([]webhookroutecmd.Item, error) {
	return o.service.Inventory(ctx, authority)
}

func (o *Operations) Get(ctx context.Context, name string, authority webhookroutecmd.Authority) (webhookroutecmd.Item, error) {
	return o.service.Get(ctx, name, authority)
}

func (o *Operations) Create(ctx context.Context, request webhookroutecmd.Create) (webhookroutecmd.SecretResult, error) {
	return o.service.Create(ctx, request)
}

func (o *Operations) Update(ctx context.Context, request webhookroutecmd.Update) (webhookroutecmd.Item, error) {
	return o.service.Update(ctx, request)
}

func (o *Operations) SetEnabled(ctx context.Context, request webhookroutecmd.ChangeSelection) (webhookroutecmd.Item, error) {
	return o.service.SetEnabled(ctx, request)
}

func (o *Operations) Delete(ctx context.Context, request webhookroutecmd.Delete) (webhookroutecmd.Item, error) {
	return o.service.Delete(ctx, request)
}

func (o *Operations) Rotate(ctx context.Context, request webhookroutecmd.Rotate) (webhookroutecmd.SecretResult, error) {
	return o.service.Rotate(ctx, request)
}
