package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandfx"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcliapp"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const pluginInstallOperation = "install"

func openPluginMaintenance(t *testing.T, dir string) (state.Provider, *catalogapp.Runtime, *pluginapp.Service) {
	t.Helper()
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	catalog, err := catalogapp.NewMaintenanceRuntime(dir, p, nil)
	if err != nil {
		t.Fatal(err)
	}
	plugins, err := pluginapp.NewManaged(dir, p.AppKV(), p.Plugins(), catalog, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	return p, catalog, plugins
}

func realSkillCLI(t *testing.T, args ...string) skillcliapp.Envelope {
	t.Helper()
	cmd := skillsCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&output)
	cmd.SetArgs(args)
	if err := cmd.ExecuteContext(t.Context()); err != nil {
		t.Fatalf("skill %v output=%q error=%v", args, output.String(), err)
	}
	var envelope skillcliapp.Envelope
	if err := json.Unmarshal(output.Bytes(), &envelope); err != nil || !envelope.OK {
		t.Fatalf("skill %v envelope=%+v decode=%v", args, envelope, err)
	}
	return envelope
}

func pluginAuditCounts(t *testing.T, p state.Provider) map[usercmd.AuditAction]int {
	t.Helper()
	page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	counts := map[usercmd.AuditAction]int{}
	for _, event := range page.Events {
		if event.TargetType == usercmd.AuditTargetPlugin {
			counts[event.Action]++
		}
	}
	return counts
}

func TestPluginCLIReinstallPublishesFreshSkillsAndPreservesSession(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	p, catalog, plugins := openPluginMaintenance(t, dir)
	if err := catalogapp.NewLifecycle(catalog, plugins).Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	baseline, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	saved := state.SessionRecord{SessionID: "existing-topic", UserID: "existing-user", ChannelType: "telegram", AddressKey: "42:7", AddressJSON: `{"chat_id":42,"topic_id":7}`, RuntimeSnapshotID: string(baseline.ID), Status: state.SessionStatusActive}
	if err := p.Sessions().Upsert(t.Context(), saved); err != nil {
		t.Fatal(err)
	}
	created, err := p.RuntimeSessions().Create(t.Context(), &adksession.CreateRequest{AppName: "plugin-cli-test", UserID: saved.UserID, SessionID: saved.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	event := adksession.NewEvent(t.Context(), "before-install")
	event.Author = "user"
	event.Content = genai.NewContentFromText("History before installation", genai.RoleUser)
	event.Actions.StateDelta = map[string]any{"saved": "keep"}
	if err := p.RuntimeSessions().AppendEvent(t.Context(), created.Session, event); err != nil {
		t.Fatal(err)
	}
	saved, _, err = p.Sessions().GetBySessionID(t.Context(), saved.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	inert := filepath.Join(dir, "plugins", "demo", "plugin.json")
	const abandoned = `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"9.9.9","description":"Unaccepted bytes"}`
	if err := writeFile(inert, abandoned); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "plugin-data", "demo", "keep.txt")
	if err := writeFile(data, "retained data"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	if output, err := executePluginCommand(t, t.Context(), "list"); err != nil || strings.Contains(output, "Name: demo") {
		t.Fatalf("unaccepted listing=%q error=%v", output, err)
	}
	for _, args := range [][]string{{"marketplace", "add", root}, {pluginInstallOperation, "demo@market"}} {
		if output, err := executePluginCommand(t, t.Context(), args...); err != nil {
			t.Fatalf("%v output=%q error=%v", args, output, err)
		}
	}
	p, _, _ = openPluginMaintenance(t, dir)
	first, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found || !first.Enabled || first.OriginMarketplace != "market" {
		t.Fatalf("first install=%+v found=%v error=%v", first, found, err)
	}
	firstRevision, found, err := p.Plugins().GetPluginRevision(t.Context(), "demo", first.ActiveRevisionID)
	if err != nil || !found {
		t.Fatalf("first revision found=%v error=%v", found, err)
	}
	if counts := pluginAuditCounts(t, p); counts[usercmd.AuditActionPluginInstalled] != 1 {
		t.Fatalf("install audits=%v", counts)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	inventory := realSkillCLI(t, "list", "--json")
	if inventory.Inventory == nil || len(inventory.Inventory.Entries) != 2 {
		t.Fatalf("fresh skill inventory=%+v", inventory)
	}
	for _, name := range []string{"review", "plan"} {
		shown := realSkillCLI(t, "show", "plugin/demo/skill/"+name, "--json")
		if shown.Entry == nil || shown.Entry.Name != name || shown.Entry.PluginID != "demo" || shown.Entry.Revision != first.ActiveRevisionID || !shown.Entry.Enabled {
			t.Fatalf("fresh skill=%+v", shown)
		}
	}
	// Same display version with different selected bytes must accept a new exact revision.
	if err := writeFile(filepath.Join(root, "plugins", "demo", "skills", "review", "SKILL.md"), "---\nname: review\ndescription: Review changed instructions.\n---\nChanged selected body.\n"); err != nil {
		t.Fatal(err)
	}
	if output, err := executePluginCommand(t, t.Context(), pluginInstallOperation, "demo@market"); err != nil {
		t.Fatalf("reinstall=%q error=%v", output, err)
	}
	p, catalog, plugins = openPluginMaintenance(t, dir)
	if err := catalog.LoadMaintenanceSnapshot(t.Context()); err != nil {
		t.Fatal(err)
	}
	current, err := catalog.Store().Application()
	if err != nil {
		t.Fatal(err)
	}
	second, found, err := p.Plugins().GetPluginInstall(t.Context(), "demo")
	if err != nil || !found || second.ActiveRevisionID == first.ActiveRevisionID || second.Version != first.Version {
		t.Fatalf("reinstall=%+v first=%+v error=%v", second, first, err)
	}
	for _, name := range []string{"review", "plan"} {
		id := runtimecatalogcmd.ContributionID{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "demo"}, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name}
		metadata, found := current.Skills[id]
		if !found || string(metadata.Revision) != second.ActiveRevisionID {
			t.Fatalf("current skill=%+v found=%v", metadata, found)
		}
	}
	pinned, err := catalog.ResolveEffectiveSnapshot(t.Context(), commandfx.SnapshotRequest{SessionID: saved.SessionID})
	if err != nil || pinned != baseline.ID {
		t.Fatalf("existing pin=%s error=%v", pinned, err)
	}
	fresh, err := catalog.ResolveEffectiveSnapshot(t.Context(), commandfx.SnapshotRequest{SessionID: "new-topic"})
	if err != nil || fresh != current.ID || fresh == pinned {
		t.Fatalf("new selection=%s old=%s error=%v", fresh, pinned, err)
	}
	stored, found, err := p.Sessions().GetBySessionID(t.Context(), saved.SessionID)
	if err != nil || !found || stored != saved {
		t.Fatalf("session metadata changed=%+v error=%v", stored, err)
	}
	history, err := p.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: "plugin-cli-test", UserID: saved.UserID, SessionID: saved.SessionID})
	if err != nil {
		t.Fatal(err)
	}
	if history.Session.Events().Len() != 1 || history.Session.Events().At(0).Content.Parts[0].Text != "History before installation" {
		t.Fatal("installation changed history")
	}
	if value, err := history.Session.State().Get("saved"); err != nil || value != "keep" {
		t.Fatalf("session state=%v error=%v", value, err)
	}
	if marker, found, err := p.AppKV().Get(t.Context(), "plugin_legacy_migration_complete"); err != nil || !found || marker != "v1" {
		t.Fatalf("migration marker=%q found=%v error=%v", marker, found, err)
	}
	for path, want := range map[string]string{inert: abandoned, data: "retained data"} {
		body, err := os.ReadFile(path)
		if err != nil || string(body) != want {
			t.Fatalf("retained bytes path=%s error=%v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, filepath.FromSlash(firstRevision.RelativeRoot))); err != nil {
		t.Fatalf("retained first revision=%v", err)
	}
	counts := pluginAuditCounts(t, p)
	if counts[usercmd.AuditActionPluginInstalled] != 1 || counts[usercmd.AuditActionPluginUpgraded] != 1 {
		t.Fatalf("mutation audits=%v", counts)
	}
	if err := plugins.Disable(t.Context(), "demo"); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	disabled := realSkillCLI(t, "list", "--json")
	if disabled.Inventory == nil || len(disabled.Inventory.Entries) != 0 {
		t.Fatalf("disabled eligible skills=%+v", disabled)
	}
	if output, err := executePluginCommand(t, t.Context(), "show", "demo"); err != nil || !strings.Contains(output, "Enabled: false") || !strings.Contains(output, "Skill: review") {
		t.Fatalf("disabled inspection=%q error=%v", output, err)
	}
}

type pluginPublicationFailure struct{ pluginapp.CatalogActivator }

func (pluginPublicationFailure) PublishCandidate(context.Context, runtimecatalogcmd.PreparedApplicationCandidate) error {
	return errors.New("synthetic publication failure")
}

func TestPluginCLIPendingPublicationRecoversThroughStartup(t *testing.T) {
	for _, operation := range []string{pluginInstallOperation, "remove"} {
		t.Run(operation, func(t *testing.T) {
			dir, root := pluginCLIFixture(t)
			for _, args := range [][]string{{"marketplace", "add", root}} {
				if output, err := executePluginCommand(t, t.Context(), args...); err != nil {
					t.Fatalf("%v output=%q error=%v", args, output, err)
				}
			}
			if operation == "remove" {
				if _, err := executePluginCommand(t, t.Context(), pluginInstallOperation, "demo@market"); err != nil {
					t.Fatal(err)
				}
			}
			p, catalog, _ := openPluginMaintenance(t, dir)
			faulted, err := pluginapp.NewManaged(dir, p.AppKV(), p.Plugins(), pluginPublicationFailure{CatalogActivator: catalog}, p.PluginManagement())
			if err != nil {
				t.Fatal(err)
			}
			original := preparePluginCLI
			preparePluginCLI = func(context.Context, bool) (*pluginapp.Service, func(), error) { return faulted, func() {}, nil }
			target := "demo@market"
			if operation == "remove" {
				target = "demo"
			}
			output, runErr := executePluginCommand(t, t.Context(), operation, target)
			preparePluginCLI = original
			if !errors.Is(runErr, plugincmd.ErrRecoveryPending) || strings.Contains(output, "Accepted and published") {
				t.Fatalf("%s output=%q error=%v", operation, output, runErr)
			}
			accepted, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil || len(accepted.Revisions) != 1 || len(accepted.Pending) != 1 {
				t.Fatalf("accepted=%+v error=%v", accepted, err)
			}
			before := pluginAuditCounts(t, p)
			if err := p.Close(); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(root); err != nil {
				t.Fatal(err)
			}
			reopened, recovered, plugins := openPluginMaintenance(t, dir)
			if err := catalogapp.NewLifecycle(recovered, plugins).Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			after, err := reopened.PluginManagement().ObservePlugin(t.Context(), "demo")
			if err != nil || len(after.Pending) != 0 || after.Generation != accepted.Generation || len(after.Revisions) != 1 || after.Revisions[0].RevisionID != accepted.Revisions[0].RevisionID {
				t.Fatalf("recovered=%+v error=%v", after, err)
			}
			if (after.Install != nil) != (operation == pluginInstallOperation) {
				t.Fatalf("recovered selection=%+v", after.Install)
			}
			counts := pluginAuditCounts(t, reopened)
			for action, count := range before {
				if counts[action] != count {
					t.Fatalf("recovery duplicated %s audit: %v -> %v", action, before, counts)
				}
			}
			snapshot, err := recovered.Store().Application()
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if operation == pluginInstallOperation {
				want = 2
			}
			if len(snapshot.Skills) != want {
				t.Fatalf("recovered skill count=%d want=%d", len(snapshot.Skills), want)
			}
		})
	}
}

func TestPluginCLIRejectsInvalidSelectedBytesAndChangedOrigin(t *testing.T) {
	dir, root := pluginCLIFixture(t)
	for _, args := range [][]string{{"marketplace", "add", root}, {pluginInstallOperation, "demo@market"}} {
		if output, err := executePluginCommand(t, t.Context(), args...); err != nil {
			t.Fatalf("%v output=%q error=%v", args, output, err)
		}
	}
	p, _, _ := openPluginMaintenance(t, dir)
	before, err := p.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	beforeAudit := pluginAuditCounts(t, p)
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(root, "plugins", "demo", "plugin.json")
	valid, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifest, []byte("invalid selected manifest"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := executePluginCommand(t, t.Context(), pluginInstallOperation, "demo@market"); err == nil {
		t.Fatal("invalid selected source accepted")
	}
	if err := os.WriteFile(manifest, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(filepath.Dir(root), "other")
	for path, body := range map[string]string{
		".agents/plugins/marketplace.json": `{"name":"other","plugins":[{"name":"demo","path":"plugins/demo"}]}`,
		"plugins/demo/plugin.json":         `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"2.0.0"}`,
	} {
		if err := writeFile(filepath.Join(other, path), body); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := executePluginCommand(t, t.Context(), "marketplace", "add", other); err != nil {
		t.Fatal(err)
	}
	if _, err := executePluginCommand(t, t.Context(), pluginInstallOperation, "demo@other"); err == nil {
		t.Fatal("locked origin replaced")
	}
	reopened, _, _ := openPluginMaintenance(t, dir)
	after, err := reopened.PluginManagement().ObservePlugin(t.Context(), "demo")
	if err != nil || after.Token() != before.Token() {
		t.Fatalf("rejected install changed accepted plugin: %+v error=%v", after, err)
	}
	counts := pluginAuditCounts(t, reopened)
	for action, count := range beforeAudit {
		if counts[action] != count {
			t.Fatalf("rejected install changed audit: %v -> %v", beforeAudit, counts)
		}
	}
}
