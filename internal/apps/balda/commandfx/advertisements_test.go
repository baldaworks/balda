package commandfx

import (
	"context"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type fakeCommandReadiness struct {
	ready map[runtimecatalogcmd.ContributionID]bool
}

func (r fakeCommandReadiness) PluginCommandReady(_ context.Context, _ runtimecatalogcmd.Snapshot, descriptor runtimecatalogcmd.CommandDescriptor) bool {
	return r.ready[descriptor.ID]
}

type recordingAdvertisementTarget struct {
	transport   string
	projections []commandcmd.AdvertisementProjection
}

func (t *recordingAdvertisementTarget) Transport() string { return t.transport }
func (t *recordingAdvertisementTarget) SupportsCommand(name string) bool {
	return !strings.Contains(name, "_")
}
func (t *recordingAdvertisementTarget) ReplaceCommands(_ context.Context, projection commandcmd.AdvertisementProjection) error {
	t.projections = append(t.projections, projection)
	return nil
}

func TestAdvertisementProjectorGatesSyntaxAndReplacesOnRefresh(t *testing.T) {
	t.Parallel()

	ready := pluginDescriptorForProjection("deploy", "revision")
	incompatible := pluginDescriptorForProjection("bad_name", "revision")
	unready := pluginDescriptorForProjection("waiting", "revision")
	omitted := pluginDescriptorForProjection("collision", "revision")
	omitted.Advertised = false
	target := &recordingAdvertisementTarget{transport: "telegram"}
	projector, err := NewAdvertisementProjector(fakeCommandReadiness{ready: map[runtimecatalogcmd.ContributionID]bool{
		ready.ID: true, incompatible.ID: true,
	}}, []AdvertisementTarget{target})
	if err != nil {
		t.Fatal(err)
	}
	snapshot := projectionSnapshot("snapshot-one", ready, incompatible, unready, omitted)
	snapshot.Sequence = 9
	if err := projector.Project(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(target.projections) != 1 || len(target.projections[0].Commands) != 1 || target.projections[0].Commands[0].Name != "deploy" {
		t.Fatalf("projection = %+v", target.projections)
	}
	if got := projector.ProjectionSequence(); got != snapshot.Sequence {
		t.Fatalf("ProjectionSequence() = %d, want %d", got, snapshot.Sequence)
	}
	codes := map[string]bool{}
	for _, diagnostic := range target.projections[0].Diagnostics {
		codes[diagnostic.Code] = true
	}
	if !codes[runtimecatalogcmd.DiagnosticCommandTransportIncompatible] || !codes[runtimecatalogcmd.DiagnosticCommandRuntimeUnavailable] {
		t.Fatalf("diagnostics = %+v", target.projections[0].Diagnostics)
	}
	status := projector.status("plugin-bad_name", "revision", snapshot.Sequence)
	if status.Advertisements != 0 || len(status.Omissions) != 1 || !strings.Contains(status.Omissions[0], runtimecatalogcmd.DiagnosticCommandTransportIncompatible) {
		t.Fatalf("incompatible command status = %+v", status)
	}

	if err := projector.Project(context.Background(), projectionSnapshot("snapshot-two")); err != nil {
		t.Fatal(err)
	}
	if len(target.projections) != 2 || target.projections[1].SnapshotID != "snapshot-two" || len(target.projections[1].Commands) != 0 {
		t.Fatalf("refresh projections = %+v", target.projections)
	}
}

func TestPinnedCommandReadinessRequiresInstruction(t *testing.T) {
	t.Parallel()

	descriptor := pluginDescriptorForProjection("deploy", "revision")
	readiness := NewPinnedCommandReadiness()
	if !readiness.PluginCommandReady(context.Background(), runtimecatalogcmd.Snapshot{}, descriptor) {
		t.Fatal("command instruction reported unavailable")
	}
	descriptor.Instruction = ""
	if readiness.PluginCommandReady(context.Background(), runtimecatalogcmd.Snapshot{}, descriptor) {
		t.Fatal("empty command instruction reported ready")
	}
}

func pluginDescriptorForProjection(name string, revision runtimecatalogcmd.RevisionID) runtimecatalogcmd.CommandDescriptor {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "plugin-" + name}
	return runtimecatalogcmd.CommandDescriptor{
		ID:       runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindCommand, Name: name},
		Revision: revision, Name: name, Description: "Run " + name, Advertised: true,
		Instruction: "Run " + name,
	}
}

func projectionSnapshot(id runtimecatalogcmd.SnapshotID, descriptors ...runtimecatalogcmd.CommandDescriptor) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{ID: id, Commands: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor)}
	for _, descriptor := range descriptors {
		snapshot.Commands[descriptor.ID] = descriptor
	}
	return snapshot
}
