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

type fakeMCPReadiness map[string]bool

func (r fakeMCPReadiness) MCPServerReady(_ runtimecatalogcmd.SourceID, revision runtimecatalogcmd.RevisionID, name string) bool {
	return r[string(revision)+"/"+name]
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
	if err := projector.Project(context.Background(), snapshot); err != nil {
		t.Fatal(err)
	}
	if len(target.projections) != 1 || len(target.projections[0].Commands) != 1 || target.projections[0].Commands[0].Name != "deploy" {
		t.Fatalf("projection = %+v", target.projections)
	}
	codes := map[string]bool{}
	for _, diagnostic := range target.projections[0].Diagnostics {
		codes[diagnostic.Code] = true
	}
	if !codes[runtimecatalogcmd.DiagnosticCommandTransportIncompatible] || !codes[runtimecatalogcmd.DiagnosticCommandRuntimeUnavailable] {
		t.Fatalf("diagnostics = %+v", target.projections[0].Diagnostics)
	}

	if err := projector.Project(context.Background(), projectionSnapshot("snapshot-two")); err != nil {
		t.Fatal(err)
	}
	if len(target.projections) != 2 || target.projections[1].SnapshotID != "snapshot-two" || len(target.projections[1].Commands) != 0 {
		t.Fatalf("refresh projections = %+v", target.projections)
	}
}

func TestPinnedCommandReadinessRequiresExactSkillAndMCPRevision(t *testing.T) {
	t.Parallel()

	descriptor := pluginDescriptorForProjection("deploy", "revision")
	skillID := runtimecatalogcmd.ContributionID{Source: descriptor.ID.Source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: descriptor.Skill.Name}
	serverID := runtimecatalogcmd.ContributionID{Source: descriptor.ID.Source, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: "tools"}
	snapshot := runtimecatalogcmd.Snapshot{
		Skills: map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.SkillMetadata{
			skillID: {ID: skillID, Revision: "revision", Name: descriptor.Skill.Name},
		},
		MCPServers: map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.MCPServerDescriptor{
			serverID: {ID: serverID, Revision: "revision", Name: "tools"},
		},
	}
	readiness := NewPinnedCommandReadiness(fakeMCPReadiness{"revision/tools": true})
	if !readiness.PluginCommandReady(context.Background(), snapshot, descriptor) {
		t.Fatal("exact pinned skill and MCP revision reported unavailable")
	}
	snapshot.MCPServers[serverID] = runtimecatalogcmd.MCPServerDescriptor{ID: serverID, Revision: "other", Name: "tools"}
	if readiness.PluginCommandReady(context.Background(), snapshot, descriptor) {
		t.Fatal("mismatched MCP revision reported ready")
	}
}

func pluginDescriptorForProjection(name string, revision runtimecatalogcmd.RevisionID) runtimecatalogcmd.CommandDescriptor {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "plugin-" + name}
	return runtimecatalogcmd.CommandDescriptor{
		ID:       runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindCommand, Name: name},
		Revision: revision, Name: name, Description: "Run " + name, Advertised: true,
		Skill: &runtimecatalogcmd.SkillRef{Source: source, Revision: revision, Name: "skill"},
	}
}

func projectionSnapshot(id runtimecatalogcmd.SnapshotID, descriptors ...runtimecatalogcmd.CommandDescriptor) runtimecatalogcmd.Snapshot {
	snapshot := runtimecatalogcmd.Snapshot{ID: id, Commands: make(map[runtimecatalogcmd.ContributionID]runtimecatalogcmd.CommandDescriptor)}
	for _, descriptor := range descriptors {
		snapshot.Commands[descriptor.ID] = descriptor
	}
	return snapshot
}
