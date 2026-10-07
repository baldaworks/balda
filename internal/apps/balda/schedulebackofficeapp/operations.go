// Package schedulebackofficeapp adapts schedule policy to Backoffice operations.
package schedulebackofficeapp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/scheduledjobs"
)

// Operations delegates schedule management to the application owner.
type Operations struct{ management *scheduledjobs.Management }

func New(management *scheduledjobs.Management) *Operations {
	return &Operations{management: management}
}

func (o *Operations) Inventory(ctx context.Context, authority schedulecmd.Authority) ([]schedulecmd.Item, error) {
	return o.management.Inventory(ctx, authority)
}

func (o *Operations) Get(ctx context.Context, id string, authority schedulecmd.Authority) (schedulecmd.Item, error) {
	return o.management.Get(ctx, id, authority)
}

func (o *Operations) Create(ctx context.Context, request schedulecmd.Create) (schedulecmd.Item, error) {
	return o.management.Create(ctx, request)
}

func (o *Operations) Update(ctx context.Context, request schedulecmd.Update) (schedulecmd.Item, error) {
	return o.management.Update(ctx, request)
}

func (o *Operations) SetEnabled(ctx context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error) {
	return o.management.SetEnabled(ctx, request)
}

func (o *Operations) Delete(ctx context.Context, request schedulecmd.Delete) (schedulecmd.Item, error) {
	return o.management.Delete(ctx, request)
}

func (o *Operations) RunNow(ctx context.Context, request schedulecmd.RunNow) (schedulecmd.RunItem, error) {
	return o.management.RunNow(ctx, request)
}

func (o *Operations) History(ctx context.Context, id string, beforeAt time.Time, beforeID string,
	limit int, authority schedulecmd.Authority) ([]schedulecmd.RunItem, error) {
	return o.management.History(ctx, id, beforeAt, beforeID, limit, authority)
}
