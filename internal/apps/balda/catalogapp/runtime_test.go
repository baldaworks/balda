package catalogapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestLifecycleMigratesLegacyPluginAndReconstructsCatalog(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir := t.TempDir()
	t.Cleanup(func() { makeWritable(stateDir) })
	writeFile(t, filepath.Join(stateDir, "plugins", "release-tools", "plugin.json"), `{
  "$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json",
  "name":"release-tools",
  "version":"1.0.0",
  "extensions":{"dev.baldaworks.balda":{"schema_version":1,"commands":[{"name":"release","description":"Release","instruction":"Deploy the release safely."}]}}
}`)
	writeFile(t, filepath.Join(stateDir, "plugins", "release-tools", "skills", "deploy", "SKILL.md"), "---\nname: deploy\ndescription: Deploy safely.\n---\n# Secret body\n")

	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	commands := commandcmd.NewRegistry()
	runtime, err := NewRuntime(stateDir, provider, []commandcmd.Advertisement{{Transport: "telegram", Enabled: true, Names: []string{"plugin", "reset"}}}, nil, mcpregistry.New(nil), commands)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewLifecycle(runtime, plugins).Start(ctx); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	install, found, err := provider.Plugins().GetPluginInstall(ctx, "release-tools")
	if err != nil || !found {
		t.Fatalf("GetPluginInstall() = (%#v, %v, %v)", install, found, err)
	}
	if install.OriginMarketplace != originUnknownForTest || install.OriginSource != originUnknownForTest || install.OriginPath != originUnknownForTest {
		t.Fatalf("legacy origin = (%q, %q, %q)", install.OriginMarketplace, install.OriginSource, install.OriginPath)
	}
	snapshot, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	assertContribution(t, snapshot, runtimecatalogcmd.SourceKindBuiltin, runtimecatalogcmd.ContributionKindCommand, "reset")
	assertContribution(t, snapshot, runtimecatalogcmd.SourceKindPlugin, runtimecatalogcmd.ContributionKindCommand, "release")
	assertContribution(t, snapshot, runtimecatalogcmd.SourceKindPlugin, runtimecatalogcmd.ContributionKindSkill, "deploy")
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review changes.\n---\n")
	current, err := runtime.CurrentSkillSnapshot(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	if current.ID == snapshot.ID {
		t.Fatal("workspace catalog did not produce a distinct current snapshot")
	}
	if err := provider.Sessions().Upsert(ctx, baldastate.SessionRecord{
		SessionID: "pinned-session", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`,
		WorkspaceDir: workspace, RuntimeSnapshotID: string(snapshot.ID),
	}); err != nil {
		t.Fatal(err)
	}
	resolved, err := runtime.ResolveEffectiveSnapshot(ctx, commandfx.SnapshotRequest{SessionID: "pinned-session"})
	if err != nil {
		t.Fatal(err)
	}
	if resolved != snapshot.ID {
		t.Fatalf("resolved snapshot = %q, want session pin %q", resolved, snapshot.ID)
	}
	if !commands.Supports("telegram", "release") {
		t.Fatal("plugin command was not projected to telegram")
	}
}

func TestRuntimeBuildsIsolatedWorkspaceOverlayAndReadsPinnedSkill(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir := t.TempDir()
	t.Cleanup(func() { makeWritable(stateDir) })
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewRuntime(stateDir, provider, []commandcmd.Advertisement{{Transport: "telegram", Enabled: true, Names: []string{"reset"}}}, nil, mcpregistry.New(nil), commandcmd.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	oldApplication, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(stateDir, "skills", "personal", "SKILL.md"), "---\nname: personal\ndescription: Personal skill.\n---\n# Personal\n")
	if err := plugins.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review changes.\n---\n# Workspace-only body\n")

	effective, err := runtime.CurrentSkillSnapshot(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	assertContribution(t, effective, runtimecatalogcmd.SourceKindWorkspaceSkill, runtimecatalogcmd.ContributionKindSkill, "review")
	application, err := runtime.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	for id := range application.Skills {
		if id.Source.Kind == runtimecatalogcmd.SourceKindWorkspaceSkill {
			t.Fatalf("application snapshot contains workspace skill: %#v", id)
		}
	}
	manager, err := baldaagent.NewSkillManager(runtime, runtime, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	bound, err := manager.BindSnapshot(ctx, effective.ID)
	if err != nil {
		t.Fatal(err)
	}
	selection, err := bound.Resolve(baldaagent.SkillSelector{Name: "review"})
	if err != nil {
		t.Fatal(err)
	}
	runtimeAfterRestart, err := NewRuntime(stateDir, provider, []commandcmd.Advertisement{{Transport: "telegram", Enabled: true, Names: []string{"reset"}}}, nil, mcpregistry.New(nil), commandcmd.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	pluginsAfterRestart, err := pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), runtimeAfterRestart)
	if err != nil {
		t.Fatal(err)
	}
	if err := pluginsAfterRestart.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if restored, err := runtimeAfterRestart.ResolveCommandSnapshot(ctx, oldApplication.ID); err != nil || restored.ID != oldApplication.ID {
		t.Fatalf("ResolveCommandSnapshot(old after restart) = (%q, %v)", restored.ID, err)
	}
	managerAfterRestart, err := baldaagent.NewSkillManager(runtimeAfterRestart, runtimeAfterRestart, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := managerAfterRestart.LoadPinned(ctx, selection, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(loaded.Instructions, "# Workspace-only body") {
		t.Fatalf("instructions = %q", loaded.Instructions)
	}
}

func TestSessionCapabilityBinderUsesExactRetainedSnapshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	stateDir := t.TempDir()
	t.Cleanup(func() { makeWritable(stateDir) })
	provider, err := baldastate.NewSQLiteProvider(ctx, filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	runtime, err := NewRuntime(stateDir, provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry())
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginapp.NewManaged(stateDir, provider.AppKV(), provider.Plugins(), runtime)
	if err != nil {
		t.Fatal(err)
	}
	if err := plugins.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	writeFile(t, filepath.Join(workspace, ".agents", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review changes.\n---\n# Review\n")
	retained, err := runtime.CurrentSkillSnapshot(ctx, workspace)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := baldaagent.NewSkillManager(runtime, runtime, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	binder := &sessionCapabilityBinder{catalog: runtime, skills: manager}

	binding, err := binder.BindSessionCapabilities(ctx, baldaagent.SessionRuntimeRequest{
		WorkspaceDir:      workspace,
		RuntimeSnapshotID: string(retained.ID),
	})
	if err != nil {
		t.Fatalf("BindSessionCapabilities() error = %v", err)
	}
	if binding.SnapshotID != retained.ID || binding.Skills.Snapshot != retained.ID {
		t.Fatalf("binding snapshots = (%q, %q), want %q", binding.SnapshotID, binding.Skills.Snapshot, retained.ID)
	}
	if len(binding.Skills.Skills) != 1 || binding.Skills.Skills[0].Name != "review" {
		t.Fatalf("bound skills = %+v, want retained review skill", binding.Skills.Skills)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := binding.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	_, err = binder.BindSessionCapabilities(ctx, baldaagent.SessionRuntimeRequest{RuntimeSnapshotID: "snapshot-missing"})
	if !errors.Is(err, runtimecatalogcmd.ErrSnapshotUnavailable) {
		t.Fatalf("BindSessionCapabilities(missing) error = %v, want ErrSnapshotUnavailable", err)
	}
}

const originUnknownForTest = "origin-unknown"

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeWritable(root string) {
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.Type()&os.ModeSymlink == 0 {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
}

func assertContribution(t *testing.T, snapshot runtimecatalogcmd.Snapshot, source runtimecatalogcmd.SourceKind, kind runtimecatalogcmd.ContributionKind, name string) {
	t.Helper()
	id := runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: source, Name: map[runtimecatalogcmd.SourceKind]string{runtimecatalogcmd.SourceKindBuiltin: "balda", runtimecatalogcmd.SourceKindPlugin: "release-tools", runtimecatalogcmd.SourceKindWorkspaceSkill: workspaceScopeNameFromSnapshot(snapshot)}[source]}, Kind: kind, Name: name}
	switch kind {
	case runtimecatalogcmd.ContributionKindCommand:
		if _, ok := snapshot.Commands[id]; !ok {
			t.Fatalf("command %s not found in %#v", id.String(), snapshot.Commands)
		}
	case runtimecatalogcmd.ContributionKindSkill:
		if _, ok := snapshot.Skills[id]; !ok {
			t.Fatalf("skill %s not found in %#v", id.String(), snapshot.Skills)
		}
	}
}

func workspaceScopeNameFromSnapshot(snapshot runtimecatalogcmd.Snapshot) string {
	for id := range snapshot.Sources {
		if id.Kind == runtimecatalogcmd.SourceKindWorkspaceSkill {
			return id.Name
		}
	}
	return ""
}
