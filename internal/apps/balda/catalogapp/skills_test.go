package catalogapp

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestManagedSkillsCompleteCatalogRecovery(t *testing.T) {
	p, catalog, plugins, mcpMutation, credentials := hybridCatalogFixture(t)
	ctx := t.Context()
	now := time.Now().UTC()
	rootPath := t.TempDir()
	writeFile(t, filepath.Join(rootPath, "picked", "SKILL.md"), "---\nname: selected\ndescription: Selected skill\n---\nOriginal body")
	writeFile(t, filepath.Join(rootPath, "picked", "references", "details.txt"), "Original resource")
	writeFile(t, filepath.Join(rootPath, "sibling", "SKILL.md"), "---\nname: sibling\ndescription: Not selected\n---\n")
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	source, err := catalog.CaptureCandidate(ctx, root, "picked", runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-selected"})
	if err != nil {
		t.Fatal(err)
	}
	if len(source.Skills) != 1 {
		t.Fatalf("captured skills=%+v", source.Skills)
	}
	metadata := source.Skills[0]
	installation := skillcmd.Installation{Descriptor: source.Descriptor, Name: metadata.Name, Description: metadata.Description, Origin: skillcmd.Origin{Kind: "local", Source: rootPath, Path: "picked"}, Enabled: true, CreatedAt: now, UpdatedAt: now}
	record := skillcmd.Record{SchemaVersion: 1, Generation: 1, Installations: []skillcmd.Installation{installation}, Pending: true, UpdatedAt: now}
	commit := func(op skillcmd.Operation) {
		t.Helper()
		observed, err := p.SkillManagement().Observe(ctx)
		if err != nil {
			t.Fatal(err)
		}
		a := mcpMutation.Authority
		err = p.SkillManagement().Commit(ctx, skillcmd.Commit{Authority: skillcmd.Authority{UserID: a.UserID, UserVersion: a.UserVersion, CredentialVersion: a.CredentialVersion, SessionID: a.SessionID, SessionVersion: a.SessionVersion, At: now}, ExpectedState: observed.Token(), Record: record, Operation: op, TargetID: installation.Descriptor.ID.Name, AuditID: strings.ReplaceAll(string(op), " ", "-") + "-" + time.Now().Format("150405.000000000")})
		if err != nil {
			t.Fatal(err)
		}
	}
	assertPublished := func(enabled bool) runtimecatalogcmd.Snapshot {
		t.Helper()
		snapshot, err := catalog.Store().Application()
		if err != nil {
			t.Fatal(err)
		}
		if _, found := snapshot.Skills[metadata.ID]; found != enabled {
			t.Fatalf("managed present=%v want=%v snapshot=%+v", found, enabled, snapshot.Skills)
		}
		observed, err := p.SkillManagement().Observe(ctx)
		if err != nil || observed.Record.Pending {
			t.Fatalf("pending=%v err=%v", observed.Record.Pending, err)
		}
		return snapshot
	}
	commit(skillcmd.OperationInstall)
	// First pending standalone installation recovers even with no plugins.
	if err := NewLifecycle(catalog, plugins).Start(ctx); err != nil {
		t.Fatal(err)
	}
	pinned := assertPublished(true)
	// Inventory reads never capture a changed unmanaged root.
	writeFile(t, filepath.Join(catalog.stateDir, "skills", "unmanaged", "SKILL.md"), "---\nname: unmanaged\ndescription: Added outside management\n---\n")
	current, err := catalog.CurrentSkillSnapshot(ctx, "")
	if err != nil || current.ID != pinned.ID {
		t.Fatalf("read changed catalog: %v %v", current.ID, err)
	}
	// Plugin recovery and a managed MCP publication rebuild all current sources.
	marketplace := t.TempDir()
	writeFile(t, filepath.Join(marketplace, "plugins", "tools", "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"tools","version":"1.0.0"}`)
	writeFile(t, filepath.Join(marketplace, "plugins", "tools", "skills", "plugin-skill", "SKILL.md"), "---\nname: plugin-skill\ndescription: Plugin skill\n---\n")
	writeFile(t, filepath.Join(marketplace, ".agents", "plugins", "marketplace.json"), `{"name":"market","plugins":[{"name":"tools","path":"./plugins/tools"}]}`)
	if err := plugins.AddMarketplace(ctx, pluginapp.MarketplaceSource{Name: "market", Source: marketplace}); err != nil {
		t.Fatal(err)
	}
	if err := plugins.Install(ctx, "tools@market"); err != nil {
		t.Fatal(err)
	}
	mixed := assertPublished(true)
	if _, found := mixed.Skills[runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "tools"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: "plugin-skill"}]; !found {
		t.Fatalf("plugin lost: %+v", mixed.Skills)
	}
	if err := catalog.PublishMCP(ctx, func() error { return p.MCP().SaveMCPConnection(ctx, mcpMutation) }); err != nil {
		t.Fatal(err)
	}
	mixed = assertPublished(true)
	if len(mixed.MCPServers) != 1 {
		t.Fatalf("MCP lost: %+v", mixed.MCPServers)
	}
	// Equal content at a later generation is different publication evidence.
	old, err := catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		t.Fatal(err)
	}
	record.Generation++
	record.Pending = true
	commit(skillcmd.OperationRefresh)
	if err := catalog.PublishCandidate(ctx, old); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("stale generation publish=%v", err)
	}
	observed, err := p.SkillManagement().Observe(ctx)
	if err != nil || !observed.Record.Pending {
		t.Fatalf("stale completed marker: %+v %v", observed, err)
	}
	fresh, err := catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		t.Fatal(err)
	}
	if fresh.ID != old.ID {
		t.Fatalf("generation changed content snapshot: %s %s", fresh.ID, old.ID)
	}
	if err := catalog.RetrySkillPublication(ctx); err != nil {
		t.Fatal(err)
	}
	assertPublished(true)
	// Omission of a selected descriptor also conflicts before current changes.
	complete, err := catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		t.Fatal(err)
	}
	missing := complete
	missing.Sources = maps.Clone(complete.Sources)
	delete(missing.Sources, source.Descriptor.ID)
	beforeMissing, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.PublishCandidate(ctx, missing); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("missing selected source evidence=%v", err)
	}
	afterMissing, err := catalog.Store().Application()
	if err != nil || afterMissing.ID != beforeMissing.ID {
		t.Fatalf("conflict changed current=%s %v", afterMissing.ID, err)
	}
	// Removal of the final skill requires explicit empty evidence. Old candidates
	// cannot resurrect it or mark that empty desired generation complete.
	stale, err := catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		t.Fatal(err)
	}
	record.Generation++
	record.Installations[0].Removed = true
	record.Installations[0].Enabled = false
	record.Pending = true
	commit(skillcmd.OperationRemove)
	if err := catalog.PublishCandidate(ctx, stale); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("removed source publish=%v", err)
	}
	candidate, err := catalog.PrepareSkillCandidate(ctx, record.Selection())
	if err != nil {
		t.Fatal(err)
	}
	// A matching fence alone cannot authorize a snapshot with extra sources.
	corrupt := candidate
	corrupt.Sources = maps.Clone(candidate.Sources)
	corrupt.Sources[source.Descriptor.ID] = source.Descriptor
	if err := catalog.PublishCandidate(ctx, corrupt); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("mismatched snapshot evidence=%v", err)
	}
	// A persistence fault after acceptance must retain the old current catalog
	// and leave the exact newly accepted empty selection pending.
	beforeFailure, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	realKV := catalog.kv
	catalog.kv = &failedSnapshotWrite{KVStore: realKV}
	if err := catalog.PublishCandidate(ctx, candidate); err == nil {
		t.Fatal("publication failure swallowed")
	}
	catalog.kv = realKV
	afterFailure, err := catalog.Store().Application()
	if err != nil || afterFailure.ID != beforeFailure.ID {
		t.Fatalf("failed persist changed current=%s %v", afterFailure.ID, err)
	}
	pendingFailure, err := p.SkillManagement().Observe(ctx)
	if err != nil || !pendingFailure.Record.Pending {
		t.Fatalf("failed persist completed accepted selection: %+v %v", pendingFailure, err)
	}
	// Publication may succeed while completion fails: current stays usable,
	// pending is visible, and a fresh rebuild reconciles current plugin/MCP state.
	realStore := catalog.managedSkills
	catalog.managedSkills = &completionFault{managedSkillStore: realStore}
	if err := catalog.PublishCandidate(ctx, candidate); err == nil {
		t.Fatal("completion fault swallowed")
	}
	observed, err = p.SkillManagement().Observe(ctx)
	if err != nil || !observed.Record.Pending {
		t.Fatalf("completion fault marker: %+v %v", observed, err)
	}
	catalog.managedSkills = realStore
	if err := plugins.Disable(ctx, "tools"); err != nil {
		t.Fatal(err)
	}
	final := assertPublished(false)
	if len(final.MCPServers) != 1 {
		t.Fatalf("MCP lost on recovery: %+v", final.MCPServers)
	}
	// A later accepted refresh remains pending across process-owner reopen.
	record.Generation++
	record.Pending = true
	commit(skillcmd.OperationRefresh)
	stateDir := catalog.stateDir
	if err := catalog.MCP().Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := state.NewSQLiteProvider(ctx, filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
	recovered, err := NewRuntime(stateDir, "", "", reopened, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), credentials, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recovered.MCP().Shutdown(ctx) })
	recoveredPlugins, err := pluginapp.NewManaged(stateDir, reopened.AppKV(), reopened.Plugins(), recovered)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewLifecycle(recovered, recoveredPlugins).Start(ctx); err != nil {
		t.Fatal(err)
	}
	currentAfterReopen, err := recovered.Store().Application()
	if err != nil || currentAfterReopen.ID != final.ID {
		t.Fatalf("reopen lost current desired catalog=%s %v", currentAfterReopen.ID, err)
	}
	completed, err := reopened.SkillManagement().Observe(ctx)
	if err != nil || completed.Record.Pending || completed.Record.Generation != record.Generation {
		t.Fatalf("reopen completion=%+v %v", completed, err)
	}
	catalog = recovered
	// Mutable origin removal cannot break an old session's exact retained reader.
	if err := os.RemoveAll(rootPath); err != nil {
		t.Fatal(err)
	}
	historical, err := catalog.RetainedSkillSnapshot(ctx, pinned.ID)
	if err != nil {
		t.Fatal(err)
	}
	oldMetadata := historical.Skills[metadata.ID]
	loaded, err := catalog.ReadSkill(ctx, runtimecatalogcmd.SkillReadRequest{Ref: runtimecatalogcmd.SkillRef{Source: metadata.ID.Source, Name: metadata.Name, Revision: metadata.Revision}, MainResource: oldMetadata.Resource, Resources: []string{"references/details.txt"}})
	if err != nil || !strings.Contains(loaded.Instructions, "Original body") {
		t.Fatalf("old exact read=%+v %v", loaded, err)
	}
	if len(loaded.Resources) != 1 || !strings.Contains(loaded.Resources[0].Content, "Original resource") {
		t.Fatalf("old resources=%+v", loaded.Resources)
	} // Archive corruption is terminal for this exact revision; never read the
	// mutable origin and never replace the current accepted catalog on failure.
	archived, err := catalog.archive.ResolveRevisionRoot(ctx, source.Descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(archived, archived+".unavailable"); err != nil {
		t.Fatal(err)
	}
	missingDesired := runtimecatalogcmd.ManagedSkillSelection{Generation: record.Generation + 1, Sources: []runtimecatalogcmd.SourceDescriptor{source.Descriptor}}
	_, missingErr := catalog.PrepareSkillCandidate(ctx, missingDesired)
	if err := os.Rename(archived+".unavailable", archived); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(missingErr, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("missing archive preparation=%v", missingErr)
	}
	makeWritable(archived)
	writeFile(t, filepath.Join(archived, "selected", "SKILL.md"), "tampered archive")
	desired := runtimecatalogcmd.ManagedSkillSelection{Generation: record.Generation + 1, Sources: []runtimecatalogcmd.SourceDescriptor{source.Descriptor}}
	if _, err := catalog.PrepareSkillCandidate(ctx, desired); !errors.Is(err, runtimecatalogcmd.ErrRevisionUnavailable) {
		t.Fatalf("damaged archive preparation=%v", err)
	}
	stillCurrent, err := catalog.Store().Application()
	if err != nil || stillCurrent.ID != final.ID {
		t.Fatalf("failed candidate changed current=%s %v", stillCurrent.ID, err)
	}
}

type completionFault struct{ managedSkillStore }

func (*completionFault) MarkPublished(context.Context, runtimecatalogcmd.ManagedSkillSelection) error {
	return errors.New("injected completion failure")
}
