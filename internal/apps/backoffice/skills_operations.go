package backoffice

import (
	"context"
	"fmt"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// SkillsInstall is the transport-neutral reviewed-install input.
type SkillsInstall = skillcmd.InstallReview

// SkillsOperations is the consuming port for administrator skill management.
type SkillsOperations interface {
	Inventory(ctx context.Context, authority skillcmd.Authority) (skillcmd.Inventory, error)
	ReviewInstall(ctx context.Context, request SkillsInstall) (skillcmd.Candidate, error)
	ReviewUpdate(ctx context.Context, request skillcmd.Mutation) (skillcmd.Candidate, error)
	Accept(ctx context.Context, handle string, authority skillcmd.Authority) (skillcmd.Outcome, error)
	Change(ctx context.Context, operation skillcmd.Operation, request skillcmd.Mutation) (skillcmd.Outcome, error)
	Refresh(ctx context.Context, request skillcmd.Mutation) (skillcmd.Outcome, error)
	RetryPublication(ctx context.Context, authority skillcmd.Authority) (skillcmd.Outcome, error)
}

// ConfigureSkillsOperations binds the shared owner before HTTP startup.
func (r *Runtime) ConfigureSkillsOperations(operations SkillsOperations) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.server != nil || operations == nil {
		return fmt.Errorf("skill operations must be configured before Backoffice start")
	}
	r.skills = operations
	return nil
}
