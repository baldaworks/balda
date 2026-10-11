package catalogapp

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestPluginReadStatusDistinguishesExactPublishedRevision(t *testing.T) {
	source := runtimecatalogcmd.Source{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "fixture"}, Revision: "r1"}}
	snapshot, err := runtimecatalog.NewCompiler().CompileApplication([]runtimecatalogcmd.Source{source})
	if err != nil {
		t.Fatal(err)
	}
	owner := &Runtime{store: runtimecatalog.NewStore()}
	if _, err := owner.store.PublishApplication(snapshot); err != nil {
		t.Fatal(err)
	}
	if status := owner.PluginStatus("fixture", "r1"); !status.RevisionCurrent || status.SnapshotID == "" {
		t.Fatalf("published revision: %+v", status)
	}
	if status := owner.PluginStatus("fixture", "r2"); status.RevisionCurrent {
		t.Fatal("unpublished selection reported current")
	}
	if status := owner.PluginStatus("another", "r1"); status.RevisionCurrent {
		t.Fatal("another source reported current")
	}
	unavailable := (&Runtime{store: runtimecatalog.NewStore()}).PluginStatus("fixture", "r1")
	if unavailable.RevisionCurrent || len(unavailable.DiagnosticCodes) != 1 || unavailable.DiagnosticCodes[0] != "catalog_unavailable" {
		t.Fatalf("unavailable catalog: %+v", unavailable)
	}
}
