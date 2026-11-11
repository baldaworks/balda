// Package skillbackofficeapp binds the shared skill owner to Backoffice's port.
package skillbackofficeapp

import (
	"context"
	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// Operations adapts browser inputs without owning lifecycle or catalog policy.
type Operations struct{ management *skillapp.Service }

// New binds the single shared skill management owner.
func New(management *skillapp.Service) *Operations { return &Operations{management: management} }
func (o *Operations) Inventory(ctx context.Context, a skillcmd.Authority) (skillcmd.Inventory, error) {
	return o.management.Inventory(ctx, a)
}
func (o *Operations) ReviewInstall(ctx context.Context, r backoffice.SkillsInstall) (skillcmd.Candidate, error) {
	return o.management.ReviewInstall(ctx, skillapp.InstallRequest{Source: r.Source, Ref: r.Ref, Path: r.Path, ExpectedState: r.ExpectedState, Authority: r.Authority})
}
func (o *Operations) ReviewUpdate(ctx context.Context, r skillcmd.Mutation) (skillcmd.Candidate, error) {
	return o.management.ReviewUpdate(ctx, r)
}
func (o *Operations) Accept(ctx context.Context, h string, a skillcmd.Authority) (skillcmd.Outcome, error) {
	return o.management.Accept(ctx, h, a)
}
func (o *Operations) Change(ctx context.Context, op skillcmd.Operation, r skillcmd.Mutation) (skillcmd.Outcome, error) {
	return o.management.Change(ctx, op, r)
}
func (o *Operations) Refresh(ctx context.Context, r skillcmd.Mutation) (skillcmd.Outcome, error) {
	return o.management.Refresh(ctx, r)
}
func (o *Operations) RetryPublication(ctx context.Context, a skillcmd.Authority) (skillcmd.Outcome, error) {
	return o.management.RetryPublication(ctx, a)
}
