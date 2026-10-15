package catalogapp

import (
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

// PluginStatus returns the catalog owner's bounded exact-revision observations.
// It reuses the existing projection policy without exposing command wiring to UI.
func (r *Runtime) PluginStatus(name, revision string) plugincmd.RuntimeStatus {
	status := commandfx.NewCatalogStatusReader(r.store, r.mcp, r.ads).Status(name, revision)
	if r.store == nil {
		return status
	}
	snapshot, err := r.store.Application()
	if err != nil || string(snapshot.ID) != status.SnapshotID {
		return status
	}
	source, found := snapshot.Sources[runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: name}]
	status.RevisionCurrent = found && string(source.Revision) == revision
	return status
}
