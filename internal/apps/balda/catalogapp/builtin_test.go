package catalogapp

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcliapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/mcpregistry"
)

func TestBuiltinSystemSkillIsCatalogMetadataAndLoadsFromRetainedRevision(t *testing.T) {
	stateDir := t.TempDir()
	t.Cleanup(func() { makeWritable(stateDir) })
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewRuntime(
		stateDir,
		"",
		"",
		provider,
		[]commandcmd.Advertisement{{Transport: "telegram", Enabled: true, Names: []string{"reset"}}},
		nil,
		mcpregistry.New(nil),
		commandcmd.NewRegistry(),
		nil,
		nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.RetainBuiltin(t.Context()); err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.PublishCandidate(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}

	skillID := runtimecatalogcmd.ContributionID{Source: builtinSourceID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: builtinSystemSkill}
	metadata, ok := candidate.Skills[skillID]
	if !ok || metadata.Resource != builtinSkillResource || metadata.Revision != runtime.builtin.Descriptor.Revision {
		t.Fatalf("built-in metadata = %+v, found=%v", metadata, ok)
	}
	commandID := runtimecatalogcmd.ContributionID{Source: builtinSourceID, Kind: runtimecatalogcmd.ContributionKindCommand, Name: "reset"}
	if command := candidate.Commands[commandID]; command.Revision != metadata.Revision {
		t.Fatalf("command revision = %q, skill revision = %q", command.Revision, metadata.Revision)
	}

	manager, err := baldaagent.NewSkillManager(runtime, runtime, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindSnapshot(t.Context(), candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	projection := bound.Metadata(baldaagent.SkillMetadataBudget{})
	if len(projection.Skills) != 1 || projection.Skills[0].Source != builtinSourceID || projection.Skills[0].Name != builtinSystemSkill {
		t.Fatalf("built-in metadata projection = %+v", projection)
	}
	if strings.Contains(projection.Skills[0].Description, "You are operating inside") || strings.Contains(projection.Skills[0].Description, "SKILL.md") {
		t.Fatalf("metadata projection exposed skill body or resource: %+v", projection.Skills[0])
	}
	remaining, explicit, err := manager.PinExplicit(t.Context(), string(candidate.ID), "$skill:builtin/balda/balda-system inspect")
	if err != nil || explicit == nil || remaining != "inspect" || explicit.Ref.Source != builtinSourceID || explicit.Ref.Name != builtinSystemSkill || explicit.Ref.Revision != metadata.Revision {
		t.Fatalf("explicit built-in selection = remaining %q selection %+v error %v", remaining, explicit, err)
	}
	selection, err := bound.Resolve(baldaagent.SkillSelector{Source: builtinSourceID, Name: builtinSystemSkill})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.LoadPinned(t.Context(), selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Ref.Revision != metadata.Revision || !strings.Contains(loaded.Instructions, "Balda channel session") {
		t.Fatalf("loaded built-in skill = %+v", loaded)
	}
	if len(loaded.Resources) != 0 {
		t.Fatalf("default built-in load returned resources = %+v", loaded.Resources)
	}
	loaded, err = manager.LoadPinned(t.Context(), selection, []string{
		"references/workspace.md",
		"references/cli.md",
		"references/sessions-and-tools.md",
	})
	if err != nil {
		t.Fatal(err)
	}
	wantResources := []string{
		"references/cli.md",
		"references/sessions-and-tools.md",
		"references/workspace.md",
	}
	if len(loaded.Resources) != len(wantResources) {
		t.Fatalf("loaded built-in resources = %+v", loaded.Resources)
	}
	for i, resource := range loaded.Resources {
		if resource.Name != wantResources[i] || strings.TrimSpace(resource.Content) == "" {
			t.Fatalf("loaded built-in resource %d = %+v, want non-empty %q", i, resource, wantResources[i])
		}
	}
	if strings.Contains(loaded.Instructions, "balda.session.progress") {
		t.Fatalf("loaded built-in instructions retain progress policy:\n%s", loaded.Instructions)
	}
	for _, resource := range loaded.Resources {
		if strings.Contains(resource.Content, "balda.session.progress") {
			t.Fatalf("loaded built-in resource %q retains progress policy:\n%s", resource.Name, resource.Content)
		}
	}

	readOnly, err := skillapp.NewReadOnly(provider.SkillManagement(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	cli, err := skillcliapp.New(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	entry, err := cli.Show(t.Context(), skillID.String())
	if err != nil || entry.Name != builtinSystemSkill || entry.Ownership != skillcmd.OwnershipUnmanaged {
		t.Fatalf("CLI built-in entry = %+v, error %v", entry, err)
	}
	_, err = cli.Change(t.Context(), skillcmd.OperationDisable, skillID.String(), func(skillcliapp.Review) (bool, error) { return true, nil })
	var public *skillcliapp.Error
	if !errors.As(err, &public) || public.Code != skillcliapp.CodeNotOwned {
		t.Fatalf("CLI built-in mutation error = %v, want %s", err, skillcliapp.CodeNotOwned)
	}
}

func TestPublishCandidateRejectsMissingBuiltinSystemSkill(t *testing.T) {
	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewRuntime(stateDir, "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	delete(candidate.Skills, runtimecatalogcmd.ContributionID{Source: builtinSourceID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: builtinSystemSkill})
	if err := runtime.PublishCandidate(t.Context(), candidate); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("PublishCandidate() error = %v, want revision unavailable", err)
	}
}

func TestPublishCandidateRejectsExtraBuiltinContribution(t *testing.T) {
	stateDir := t.TempDir()
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewRuntime(stateDir, "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := runtime.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	extraID := runtimecatalogcmd.ContributionID{Source: builtinSourceID, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "unexpected"}
	candidate.Skills[extraID] = runtimecatalogcmd.SkillMetadata{
		ID: extraID, Revision: runtime.builtin.Descriptor.Revision, Name: "unexpected", Resource: "unexpected/SKILL.md",
	}
	if err := runtime.PublishCandidate(t.Context(), candidate); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("PublishCandidate() error = %v, want revision unavailable", err)
	}
}
