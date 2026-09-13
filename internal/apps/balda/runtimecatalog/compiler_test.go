package runtimecatalog

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

func TestCompileApplicationIsDeterministic(t *testing.T) {
	t.Parallel()
	const changedRevision = "changed-revision"

	builtin := commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "builtin-revision", "reset")
	plugin := pluginSource("release-tools", "plugin-revision", "release", "deploy")
	compiler := NewCompiler()

	first, err := compiler.CompileApplication([]runtimecatalogcmd.Source{plugin, builtin})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	second, err := compiler.CompileApplication([]runtimecatalogcmd.Source{builtin, plugin})
	if err != nil {
		t.Fatalf("CompileApplication() reordered error = %v", err)
	}
	if first.ID != second.ID {
		t.Fatalf("reordered snapshot IDs differ: %q != %q", first.ID, second.ID)
	}

	plugin.Descriptor.Revision = changedRevision
	for i := range plugin.Commands {
		plugin.Commands[i].Revision = changedRevision
		plugin.Commands[i].Skill.Revision = changedRevision
	}
	for i := range plugin.Skills {
		plugin.Skills[i].Revision = changedRevision
	}
	changed, err := compiler.CompileApplication([]runtimecatalogcmd.Source{builtin, plugin})
	if err != nil {
		t.Fatalf("CompileApplication() changed error = %v", err)
	}
	if changed.ID == first.ID {
		t.Fatalf("changed revision retained snapshot ID %q", changed.ID)
	}
}

func TestCompileApplicationRejectsUnavailableCommandSkill(t *testing.T) {
	t.Parallel()

	source := pluginSource("release-tools", "revision", "release", "deploy")
	source.Skills = nil
	if _, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{source}); err == nil {
		t.Fatal("CompileApplication() error = nil, want unavailable skill error")
	}
}

func TestCompileApplicationRejectsNonLocalPluginCommandSkill(t *testing.T) {
	t.Parallel()

	plugin := pluginSource("release-tools", "plugin-revision", "release", "deploy")
	user := skillSource(runtimecatalogcmd.SourceKindUserSkill, "user", "user-revision", "deploy")
	plugin.Commands[0].Skill = &runtimecatalogcmd.SkillRef{
		Source: user.Descriptor.ID, Revision: user.Descriptor.Revision, Name: "deploy",
	}
	if _, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{plugin, user}); err == nil {
		t.Fatal("CompileApplication() error = nil, want non-local plugin skill error")
	}
}

func TestCompileApplicationReservesBuiltinCommandNames(t *testing.T) {
	t.Parallel()

	builtin := commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "builtin-revision", "reset")
	plugin := pluginSource("replacement", "plugin-revision", "reset", "reset-skill")
	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{plugin, builtin})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}

	for id, command := range snapshot.Commands {
		if id.Source.Kind == runtimecatalogcmd.SourceKindBuiltin && !command.Advertised {
			t.Errorf("built-in command advertised = false")
		}
		if id.Source.Kind == runtimecatalogcmd.SourceKindPlugin && command.Advertised {
			t.Errorf("colliding plugin command advertised = true")
		}
	}
	if !hasDiagnostic(snapshot.Diagnostics, runtimecatalogcmd.DiagnosticBuiltinCommandReserved) {
		t.Fatalf("diagnostics = %+v, want %q", snapshot.Diagnostics, runtimecatalogcmd.DiagnosticBuiltinCommandReserved)
	}
}

func TestCompileApplicationRejectsDuplicateBuiltinAliases(t *testing.T) {
	t.Parallel()

	_, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "first", "one", "reset"),
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "second", "two", "reset"),
	})
	if err == nil {
		t.Fatal("CompileApplication() error = nil, want duplicate built-in error")
	}
}

func TestCompileApplicationOmitsAmbiguousPluginAliases(t *testing.T) {
	t.Parallel()

	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		pluginSource("first", "one", "release", "deploy"),
		pluginSource("second", "two", "release", "ship"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	for _, command := range snapshot.Commands {
		if command.Advertised {
			t.Errorf("ambiguous command %+v advertised = true", command.ID)
		}
	}
	if got := countDiagnostics(snapshot.Diagnostics, runtimecatalogcmd.DiagnosticDuplicateCommandAlias); got != 2 {
		t.Fatalf("duplicate alias diagnostics = %d, want 2", got)
	}
}

func TestMergeKeepsWorkspaceOverlaysIsolated(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	application, err := compiler.CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "builtin", "reset"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	alpha, err := compiler.CompileWorkspace("alpha", []runtimecatalogcmd.Source{skillSource(runtimecatalogcmd.SourceKindWorkspaceSkill, "alpha", "alpha-revision", "alpha-skill")})
	if err != nil {
		t.Fatalf("CompileWorkspace(alpha) error = %v", err)
	}
	beta, err := compiler.CompileWorkspace("beta", []runtimecatalogcmd.Source{skillSource(runtimecatalogcmd.SourceKindWorkspaceSkill, "beta", "beta-revision", "beta-skill")})
	if err != nil {
		t.Fatalf("CompileWorkspace(beta) error = %v", err)
	}

	alphaEffective, err := compiler.Merge(application, alpha)
	if err != nil {
		t.Fatalf("Merge(alpha) error = %v", err)
	}
	betaEffective, err := compiler.Merge(application, beta)
	if err != nil {
		t.Fatalf("Merge(beta) error = %v", err)
	}
	if alphaEffective.ID == betaEffective.ID {
		t.Fatalf("workspace effective IDs both = %q", alphaEffective.ID)
	}
	if !hasSkill(alphaEffective, "alpha-skill") || hasSkill(alphaEffective, "beta-skill") {
		t.Fatalf("alpha skills = %+v", alphaEffective.Skills)
	}
	if !hasSkill(betaEffective, "beta-skill") || hasSkill(betaEffective, "alpha-skill") {
		t.Fatalf("beta skills = %+v", betaEffective.Skills)
	}
}

func TestMergeRejectsTamperedParentSnapshot(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	application, err := compiler.CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "builtin", "reset"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	workspace, err := compiler.CompileWorkspace("alpha", nil)
	if err != nil {
		t.Fatalf("CompileWorkspace() error = %v", err)
	}
	application.Commands = nil
	if _, err := compiler.Merge(application, workspace); err == nil {
		t.Fatal("Merge() error = nil, want tampered snapshot error")
	}
}

func TestCompileRejectsUnsafeResourceReferences(t *testing.T) {
	t.Parallel()

	tests := []string{"/host/secret", "../outside", "."}
	for _, resource := range tests {
		t.Run(resource, func(t *testing.T) {
			t.Parallel()
			source := skillSource(runtimecatalogcmd.SourceKindUserSkill, "user", "revision", "deploy")
			source.Skills[0].Resource = resource
			if _, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{source}); err == nil {
				t.Fatalf("CompileApplication(resource=%q) error = nil", resource)
			}
		})
	}
}

func TestSelectSkillMetadataIsDeterministicAndWhole(t *testing.T) {
	t.Parallel()

	compiler := NewCompiler()
	first, err := compiler.CompileApplication([]runtimecatalogcmd.Source{
		skillSource(runtimecatalogcmd.SourceKindUserSkill, "zeta", "z", "z-skill"),
		skillSource(runtimecatalogcmd.SourceKindUserSkill, "alpha", "a", "a-skill"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	second, err := compiler.CompileApplication([]runtimecatalogcmd.Source{
		skillSource(runtimecatalogcmd.SourceKindUserSkill, "alpha", "a", "a-skill"),
		skillSource(runtimecatalogcmd.SourceKindUserSkill, "zeta", "z", "z-skill"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() reordered error = %v", err)
	}

	firstSelection := SelectSkillMetadata(first, MetadataBudget{MaxItems: 1})
	secondSelection := SelectSkillMetadata(second, MetadataBudget{MaxItems: 1})
	if !reflect.DeepEqual(firstSelection, secondSelection) {
		t.Fatalf("reordered selections differ:\nfirst=%+v\nsecond=%+v", firstSelection, secondSelection)
	}
	if len(firstSelection.Skills) != 1 || firstSelection.Skills[0].Name != "a-skill" || firstSelection.Omitted != 1 {
		t.Fatalf("selection = %+v, want complete a-skill and one omission", firstSelection)
	}
}

func TestSelectSkillMetadataHonorsByteBudgetWithoutPartialDescriptors(t *testing.T) {
	t.Parallel()

	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		skillSource(runtimecatalogcmd.SourceKindUserSkill, "user", "revision", "deploy"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	selection := SelectSkillMetadata(snapshot, MetadataBudget{MaxBytes: 1})
	if len(selection.Skills) != 0 || selection.Omitted != 1 {
		t.Fatalf("selection = %+v, want one whole descriptor omitted", selection)
	}
	if !hasDiagnostic(selection.Diagnostics, runtimecatalogcmd.DiagnosticSkillMetadataOmitted) {
		t.Fatalf("diagnostics = %+v, want metadata omission", selection.Diagnostics)
	}
}

func TestStoreRetainsImmutableSnapshots(t *testing.T) {
	t.Parallel()

	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "revision", "reset"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	store := NewStore()
	retained, err := store.PublishApplication(snapshot)
	if err != nil {
		t.Fatalf("PublishApplication() error = %v", err)
	}
	for id := range retained.Commands {
		delete(retained.Commands, id)
	}
	loaded, err := store.Application()
	if err != nil {
		t.Fatalf("Application() error = %v", err)
	}
	if len(loaded.Commands) != 1 || loaded.Sequence == 0 {
		t.Fatalf("Application() = %+v, want retained command and sequence", loaded)
	}
	if _, err := store.Get("missing"); !errors.Is(err, ErrSnapshotUnavailable) {
		t.Fatalf("Get(missing) error = %v, want ErrSnapshotUnavailable", err)
	}
}

func TestStoreRejectsTamperedSnapshot(t *testing.T) {
	t.Parallel()

	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "revision", "reset"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	snapshot.Commands = nil
	if _, err := NewStore().Publish(snapshot); err == nil {
		t.Fatal("Publish() error = nil, want content mismatch")
	}
}

func TestStoreConcurrentReadsReturnIndependentCopies(t *testing.T) {
	t.Parallel()

	snapshot, err := NewCompiler().CompileApplication([]runtimecatalogcmd.Source{
		commandSource(runtimecatalogcmd.SourceKindBuiltin, "balda", "revision", "reset"),
	})
	if err != nil {
		t.Fatalf("CompileApplication() error = %v", err)
	}
	store := NewStore()
	if _, err := store.PublishApplication(snapshot); err != nil {
		t.Fatalf("PublishApplication() error = %v", err)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			loaded, loadErr := store.Get(snapshot.ID)
			if loadErr != nil {
				t.Errorf("Get() error = %v", loadErr)
				return
			}
			for id := range loaded.Commands {
				delete(loaded.Commands, id)
			}
		}()
	}
	wg.Wait()
	loaded, err := store.Get(snapshot.ID)
	if err != nil {
		t.Fatalf("Get() after readers error = %v", err)
	}
	if len(loaded.Commands) != 1 {
		t.Fatalf("retained commands = %d, want 1", len(loaded.Commands))
	}
}

func commandSource(kind runtimecatalogcmd.SourceKind, sourceName, revision, commandName string) runtimecatalogcmd.Source {
	sourceID := runtimecatalogcmd.SourceID{Kind: kind, Name: sourceName}
	id := runtimecatalogcmd.ContributionID{Source: sourceID, Kind: runtimecatalogcmd.ContributionKindCommand, Name: commandName}
	return runtimecatalogcmd.Source{
		Descriptor: runtimecatalogcmd.SourceDescriptor{ID: sourceID, Revision: runtimecatalogcmd.RevisionID(revision)},
		Commands:   []runtimecatalogcmd.CommandDescriptor{{ID: id, Revision: runtimecatalogcmd.RevisionID(revision), Name: commandName}},
	}
}

func skillSource(kind runtimecatalogcmd.SourceKind, sourceName, revision, skillName string) runtimecatalogcmd.Source {
	sourceID := runtimecatalogcmd.SourceID{Kind: kind, Name: sourceName}
	id := runtimecatalogcmd.ContributionID{Source: sourceID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: skillName}
	return runtimecatalogcmd.Source{
		Descriptor: runtimecatalogcmd.SourceDescriptor{ID: sourceID, Revision: runtimecatalogcmd.RevisionID(revision)},
		Skills: []runtimecatalogcmd.SkillMetadata{{
			ID: id, Revision: runtimecatalogcmd.RevisionID(revision), Name: skillName, Description: "Skill " + skillName, Resource: "skills/" + skillName + "/SKILL.md",
		}},
	}
}

func pluginSource(sourceName, revision, commandName, skillName string) runtimecatalogcmd.Source {
	source := skillSource(runtimecatalogcmd.SourceKindPlugin, sourceName, revision, skillName)
	commandID := runtimecatalogcmd.ContributionID{Source: source.Descriptor.ID, Kind: runtimecatalogcmd.ContributionKindCommand, Name: commandName}
	source.Commands = []runtimecatalogcmd.CommandDescriptor{{
		ID: commandID, Revision: runtimecatalogcmd.RevisionID(revision), Name: commandName, Description: "Command " + commandName,
		Skill: &runtimecatalogcmd.SkillRef{Source: source.Descriptor.ID, Revision: runtimecatalogcmd.RevisionID(revision), Name: skillName},
	}}
	return source
}

func hasDiagnostic(diagnostics []runtimecatalogcmd.Diagnostic, code string) bool {
	return countDiagnostics(diagnostics, code) > 0
}

func countDiagnostics(diagnostics []runtimecatalogcmd.Diagnostic, code string) int {
	count := 0
	for _, diagnostic := range diagnostics {
		if diagnostic.Code == code {
			count++
		}
	}
	return count
}

func hasSkill(snapshot runtimecatalogcmd.Snapshot, name string) bool {
	for _, skill := range snapshot.Skills {
		if skill.Name == name {
			return true
		}
	}
	return false
}
