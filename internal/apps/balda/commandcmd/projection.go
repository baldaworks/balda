package commandcmd

import (
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// ProjectedCommand is one transport-neutral dynamic command advertisement.
type ProjectedCommand struct {
	Name        string
	Description string
	ID          runtimecatalogcmd.ContributionID
	Revision    runtimecatalogcmd.RevisionID
}

// AdvertisementProjection atomically replaces one transport's dynamic command set.
type AdvertisementProjection struct {
	SnapshotID  runtimecatalogcmd.SnapshotID
	Transport   string
	Commands    []ProjectedCommand
	Diagnostics []runtimecatalogcmd.Diagnostic
}
