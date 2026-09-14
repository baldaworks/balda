package commandfx

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalog"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestCatalogStatusReaderReportsBoundedCatalogAndProjectionState(t *testing.T) {
	t.Parallel()

	sourceID := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}
	skillID := runtimecatalogcmd.ContributionID{Source: sourceID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "deploy"}
	commandID := runtimecatalogcmd.ContributionID{Source: sourceID, Kind: runtimecatalogcmd.ContributionKindCommand, Name: "deploy"}
	mcpID := runtimecatalogcmd.ContributionID{Source: sourceID, Kind: runtimecatalogcmd.ContributionKindMCPServer, Name: "tools"}
	revision := runtimecatalogcmd.RevisionID("revision-1")
	source := runtimecatalogcmd.Source{
		Descriptor: runtimecatalogcmd.SourceDescriptor{ID: sourceID, Revision: revision},
		Commands: []runtimecatalogcmd.CommandDescriptor{{
			ID: commandID, Revision: revision, Name: "deploy", Advertised: true,
			Skill: &runtimecatalogcmd.SkillRef{Source: sourceID, Revision: revision, Name: "deploy"},
		}},
		Skills: []runtimecatalogcmd.SkillMetadata{{
			ID: skillID, Revision: revision, Name: "deploy", Resource: "skills/deploy/SKILL.md",
		}},
		MCPServers: []runtimecatalogcmd.MCPServerDescriptor{{
			ID: mcpID, Revision: revision, Name: "tools", Transport: "stdio", ConfigRef: "mcp.json#tools",
		}},
		Diagnostics: []runtimecatalogcmd.Diagnostic{{
			Severity: runtimecatalogcmd.DiagnosticSeverityWarning, Code: "bounded_code", Source: sourceID,
		}},
	}
	unrelatedSkill := func(sourceName, revisionName string) runtimecatalogcmd.Source {
		unrelatedSource := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: sourceName}
		unrelatedID := runtimecatalogcmd.ContributionID{Source: unrelatedSource, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "unrelated"}
		return runtimecatalogcmd.Source{
			Descriptor: runtimecatalogcmd.SourceDescriptor{ID: unrelatedSource, Revision: runtimecatalogcmd.RevisionID(revisionName)},
			Skills: []runtimecatalogcmd.SkillMetadata{{
				ID: unrelatedID, Revision: runtimecatalogcmd.RevisionID(revisionName), Name: "unrelated", Resource: "SKILL.md",
			}},
		}
	}
	snapshot, err := runtimecatalog.NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		source, unrelatedSkill("user-one", "user-revision-1"), unrelatedSkill("user-two", "user-revision-2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	store := runtimecatalog.NewStore()
	retained, err := store.PublishApplication(snapshot)
	if err != nil {
		t.Fatal(err)
	}

	status := NewCatalogStatusReader(store, nil, nil).Status("demo", "revision-1")
	if status.SnapshotID != string(retained.ID) || status.SnapshotSequence != retained.Sequence {
		t.Fatalf("snapshot status = %+v, want %q/%d", status, retained.ID, retained.Sequence)
	}
	if status.Advertisements != 0 || status.Skills != 1 || status.DesiredMCPServers != 1 || status.DegradedMCPServers != 1 {
		t.Fatalf("catalog counts = %+v", status)
	}
	if status.SkillAmbiguities != 0 {
		t.Fatalf("unrelated duplicate skills affected plugin status: %+v", status)
	}
	if status.ProjectionLag != retained.Sequence {
		t.Fatalf("projection lag = %d, want %d", status.ProjectionLag, retained.Sequence)
	}
	if len(status.DiagnosticCodes) != 1 || status.DiagnosticCodes[0] != "bounded_code" {
		t.Fatalf("diagnostic codes = %v", status.DiagnosticCodes)
	}
	if len(status.ProjectionOmissions) != 2 {
		t.Fatalf("projection omissions = %v", status.ProjectionOmissions)
	}
}
