package runtimecatalogcmd

import "testing"

func TestStructuredIdentitiesDoNotDependOnPresentationDelimiters(t *testing.T) {
	t.Parallel()

	left := SourceID{Kind: SourceKindPlugin, Name: "alpha/beta"}
	right := SourceID{Kind: SourceKind("plugin/alpha"), Name: "beta"}
	if left == right {
		t.Fatalf("structured source IDs compare equal: left=%+v right=%+v", left, right)
	}

	leftContribution := ContributionID{Source: left, Kind: ContributionKindSkill, Name: "deploy/release"}
	rightContribution := ContributionID{Source: left, Kind: ContributionKind("skill/deploy"), Name: "release"}
	if leftContribution == rightContribution {
		t.Fatalf("structured contribution IDs compare equal: left=%+v right=%+v", leftContribution, rightContribution)
	}
}

func TestSnapshotCloneDoesNotShareMutableState(t *testing.T) {
	t.Parallel()
	const changed = "changed"

	sourceID := SourceID{Kind: SourceKindPlugin, Name: "demo"}
	commandID := ContributionID{Source: sourceID, Kind: ContributionKindCommand, Name: "release"}
	hookID := ContributionID{Source: sourceID, Kind: ContributionKindHook, Name: "guard"}
	snapshot := Snapshot{
		Parents:  []SnapshotID{"parent"},
		Sources:  map[SourceID]SourceDescriptor{sourceID: {ID: sourceID, Revision: "revision"}},
		Commands: map[ContributionID]CommandDescriptor{commandID: {ID: commandID, Revision: "revision", Name: "release", Instruction: "release safely"}},
		Hooks: map[ContributionID]HookDescriptor{hookID: {
			ID: hookID, Revision: "revision", Name: "guard", Description: "Guard", Event: "PreToolUse",
			Executable: "hooks/guard", Args: []string{"--one"}, Providers: []string{"codex"}, TimeoutSeconds: 30,
		}},
		Diagnostics: []Diagnostic{{
			Severity: DiagnosticSeverityWarning, Code: "test", Source: sourceID, Contribution: &commandID,
		}},
	}

	clone := snapshot.Clone()
	clone.Parents[0] = changed
	delete(clone.Sources, sourceID)
	delete(clone.Commands, commandID)
	clonedHook := clone.Hooks[hookID]
	clonedHook.Args[0] = changed
	clonedHook.Providers[0] = changed
	clone.Diagnostics[0].Contribution.Name = changed

	if snapshot.Parents[0] != "parent" || len(snapshot.Sources) != 1 {
		t.Fatalf("clone mutated snapshot containers: %+v", snapshot)
	}
	if len(snapshot.Commands) != 1 {
		t.Fatalf("clone mutated command descriptors: %+v", snapshot.Commands)
	}
	if got := snapshot.Hooks[hookID]; got.Args[0] != "--one" || got.Providers[0] != "codex" {
		t.Fatalf("clone mutated hook descriptor: %+v", got)
	}
	if got := snapshot.Diagnostics[0].Contribution.Name; got != "release" {
		t.Fatalf("clone mutated diagnostic contribution = %q, want release", got)
	}
}
