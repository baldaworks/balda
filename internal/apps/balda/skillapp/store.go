// Package skillapp owns standalone skill management policy behind local ports.
package skillapp

import (
	"context"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// ManagementStore is the application-owned consuming port for guarded metadata.
// ReadSelections is origin-free and also consumed directly by the catalog owner.
type ManagementStore interface {
	CheckAuthority(ctx context.Context, authority skillcmd.Authority) error
	Observe(ctx context.Context) (skillcmd.Observation, error)
	Commit(ctx context.Context, mutation skillcmd.Commit) error
	ReadSelections(ctx context.Context) (runtimecatalogcmd.ManagedSkillSelection, error)
	MarkPublished(ctx context.Context, evidence runtimecatalogcmd.ManagedSkillSelection) error
}
