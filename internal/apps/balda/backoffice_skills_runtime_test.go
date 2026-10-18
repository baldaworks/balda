package balda

import (
	"context"
	"errors"
	"fmt"
	adksession "google.golang.org/adk/v2/session"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/pluginapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

// TestBackofficeSkillsRuntimeContracts exercises production SQLite, shared Fx
// catalog owners, ADK capability binding and the ordinary session lifecycle.
func TestBackofficeSkillsRuntimeContracts(t *testing.T) {
	for _, git := range []bool{false, true} {
		t.Run(fmt.Sprint("git=", git), func(t *testing.T) {
			f := newSkillsRuntimeFixture(t, git)
			f.write(t, "v1")
			candidate := f.review(t)
			if candidate.SkillPath != "skills/release" || (git && candidate.ResolvedCommit == "") {
				t.Fatalf("review=%+v", candidate)
			}
			outcome, err := f.skills.Accept(t.Context(), candidate.Handle, f.authority())
			if err != nil || !outcome.Accepted || outcome.Pending {
				t.Fatalf("accept=%+v %v", outcome, err)
			}
			id := outcome.Entry.ID.Source
			old := f.createSession(t, "skills-old")
			pin := f.record(t, old).RuntimeSnapshotID
			f.seedHistory(t, old)
			f.assertSkill(t, pin, id, "v1")
			f.assertHistory(t, old)
			f.write(t, "v2")
			candidate, err = f.skills.ReviewUpdate(t.Context(), f.mutation(t, id))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = f.skills.Accept(t.Context(), candidate.Handle, f.authority()); err != nil {
				t.Fatal(err)
			}
			fresh := f.createSession(t, "skills-fresh")
			f.assertSkill(t, f.record(t, fresh).RuntimeSnapshotID, id, "v2")
			f.assertSkill(t, pin, id, "v1")
			f.assertHistory(t, old)
			if f.record(t, old).RuntimeSnapshotID != pin {
				t.Fatal("update repinned existing session")
			}
			stale := f.mutation(t, id)
			f.change(t, skillcmd.OperationDisable, id)
			if _, err = f.skills.Change(t.Context(), skillcmd.OperationEnable, stale); !errors.Is(err, skillcmd.ErrConflict) {
				t.Fatalf("stale=%v", err)
			}
			disabled := f.createSession(t, "skills-disabled")
			f.assertMissing(t, f.record(t, disabled).RuntimeSnapshotID, id)
			f.assertSkill(t, pin, id, "v1")
			f.assertHistory(t, old)
			// An explicit update keeps disabled state and origin identity.
			f.write(t, "v3")
			candidate, err = f.skills.ReviewUpdate(t.Context(), f.mutation(t, id))
			if err != nil {
				t.Fatal(err)
			}
			outcome, err = f.skills.Accept(t.Context(), candidate.Handle, f.authority())
			if err != nil || outcome.Entry.Enabled {
				t.Fatalf("disabled update=%+v %v", outcome, err)
			}
			// State changes and recovery use archives, never mutable origins.
			if err = os.RemoveAll(f.source); err != nil {
				t.Fatal(err)
			}
			f.change(t, skillcmd.OperationEnable, id)
			enabled := f.createSession(t, "skills-enabled")
			f.assertSkill(t, f.record(t, enabled).RuntimeSnapshotID, id, "v3")
			f.change(t, skillcmd.OperationRemove, id)
			f.assertSkill(t, pin, id, "v1")
			f.assertHistory(t, old)
			f.reopen(t)
			restored, err := f.sessions.RestoreSession(t.Context(), session.SessionContext{Locator: old})
			if err != nil || restored.GetRuntimeSnapshotID() != pin {
				t.Fatalf("restore=%+v %v", restored, err)
			}
			f.assertSkill(t, pin, id, "v1")
			f.assertHistory(t, old)
			if err = f.sessions.ResetSession(t.Context(), old); err != nil {
				t.Fatal(err)
			}
			if _, err = f.sessions.EnsureSession(t.Context(), session.SessionContext{Locator: old, UserID: "actor"}, pluginsRuntimeFixtureID); err != nil {
				t.Fatal(err)
			}
			f.assertMissing(t, f.record(t, old).RuntimeSnapshotID, id)
		})
	}
	t.Run("guarded acceptance and current-desired recovery", exerciseSkillsRecovery)
	t.Run("unmanaged explicit refresh", exerciseSkillsRefresh)
	t.Run("plugin source and duplicate qualification", exerciseSkillsMixedSources)
}

type skillsRuntimeFixture struct {
	*pluginsRuntimeFixture
	skills *skillapp.Service
	gate   *skillsCatalogFault
	store  *skillsStoreFault
}

type skillsCatalogFault struct {
	skillapp.Catalog
	failPrepare, failPublish bool
	entered                  chan struct{}
}

func (c *skillsCatalogFault) LockCatalogMutation(ctx context.Context) (func(), error) {
	if c.entered != nil {
		select {
		case c.entered <- struct{}{}:
		default:
		}
	}
	return c.Catalog.LockCatalogMutation(ctx)
}
func (c *skillsCatalogFault) PrepareSkillCandidate(ctx context.Context, s runtimecatalogcmd.ManagedSkillSelection) (runtimecatalogcmd.PreparedApplicationCandidate, error) {
	if c.failPrepare {
		return runtimecatalogcmd.PreparedApplicationCandidate{}, errors.New("synthetic compile fault")
	}
	return c.Catalog.PrepareSkillCandidate(ctx, s)
}
func (c *skillsCatalogFault) PublishCandidate(ctx context.Context, p runtimecatalogcmd.PreparedApplicationCandidate) error {
	if c.failPublish {
		return errors.New("synthetic publication fault")
	}
	return c.Catalog.PublishCandidate(ctx, p)
}

type skillsStoreFault struct {
	skillapp.ManagementStore
	fail bool
}

func (s *skillsStoreFault) Commit(ctx context.Context, m skillcmd.Commit) error {
	if s.fail {
		return errors.New("synthetic audit fault")
	}
	return s.ManagementStore.Commit(ctx, m)
}
func newSkillsRuntimeFixture(t *testing.T, git bool) *skillsRuntimeFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("CODEX_HOME", t.TempDir())
	f := &skillsRuntimeFixture{pluginsRuntimeFixture: newPluginsRuntimeFixture(t, git)}
	f.bind(t)
	return f
}
func (f *skillsRuntimeFixture) bind(t *testing.T) {
	t.Helper()
	f.runtime.service = f.provider.RuntimeSessions()
	f.gate = &skillsCatalogFault{Catalog: f.catalog}
	f.store = &skillsStoreFault{ManagementStore: f.provider.SkillManagement()}
	var err error
	f.skills, err = skillapp.New(f.dir, f.store, f.gate)
	if err != nil {
		t.Fatal(err)
	}
}
func (f *skillsRuntimeFixture) reopen(t *testing.T) {
	t.Helper()
	f.pluginsRuntimeFixture.reopen(t)
	f.bind(t)
}
func (f *skillsRuntimeFixture) authority() skillcmd.Authority {
	a := f.pluginsRuntimeFixture.authority
	return skillcmd.NewBrowserAuthority(a.UserID, a.UserVersion, a.CredentialVersion, a.MFAVersion, a.SessionID, a.SessionVersion, a.At)
}
func (f *skillsRuntimeFixture) inventory(t *testing.T) skillcmd.Inventory {
	t.Helper()
	i, e := f.skills.Inventory(t.Context(), f.authority())
	if e != nil {
		t.Fatal(e)
	}
	return i
}
func (f *skillsRuntimeFixture) mutation(t *testing.T, id runtimecatalogcmd.SourceID) skillcmd.Mutation {
	t.Helper()
	return skillcmd.Mutation{ID: id, ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority()}
}
func (f *skillsRuntimeFixture) review(t *testing.T) skillcmd.Candidate {
	t.Helper()
	c, e := f.skills.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: f.definitionSource(), Ref: f.sourceRef(), Path: "skills/release", ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority()})
	if e != nil {
		t.Fatal(e)
	}
	return c
}
func (f *skillsRuntimeFixture) change(t *testing.T, op skillcmd.Operation, id runtimecatalogcmd.SourceID) {
	t.Helper()
	out, e := f.skills.Change(t.Context(), op, f.mutation(t, id))
	if e != nil || !out.Accepted || out.Pending {
		observation, _ := f.store.Observe(t.Context())
		for _, i := range observation.Record.Installations {
			t.Logf("installation %s created=%s updated=%s now=%s validate=%v", i.Descriptor.ID.Name, i.CreatedAt, i.UpdatedAt, time.Now(), observation.Record.Validate())
		}
		t.Fatalf("%s=%+v %v", op, out, e)
	}
}
func (f *skillsRuntimeFixture) write(t *testing.T, version string) {
	t.Helper()
	for name, body := range map[string]string{"skills/release/SKILL.md": "---\nname: release\ndescription: Release safely.\n---\nbody-" + version + "\n", "skills/release/references/check.txt": "resource-" + version, "skills/other/SKILL.md": "---\nname: other\ndescription: Other skill.\n---\nnot selected\n"} {
		p := filepath.Join(f.source, name)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if f.git {
		if version == "v1" {
			f.runGit(t, "init", "-b", pluginsRuntimeFixtureID)
			f.runGit(t, "config", "user.name", "Skills Fixture")
			f.runGit(t, "config", "user.email", "skills@example.test")
		}
		f.runGit(t, "add", ".")
		f.runGit(t, "commit", "-m", "fixture "+version)
	}
}
func (f *skillsRuntimeFixture) assertSkill(t *testing.T, pin string, id runtimecatalogcmd.SourceID, version string) {
	t.Helper()
	snapshot := f.snapshot(t, pin)
	var count int
	for _, m := range snapshot.Skills {
		if m.ID.Source != id {
			continue
		}
		count++
		ref := runtimecatalogcmd.SkillRef{Source: id, Revision: m.Revision, Name: m.Name}
		loaded, e := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: ref, MainResource: m.Resource})
		if e != nil || !strings.Contains(loaded.Instructions, "body-"+version) {
			t.Fatalf("retained body=%+v %v", loaded, e)
		}
		resource, e := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: ref, MainResource: m.Resource, Resources: []string{"references/check.txt"}})
		if e != nil || len(resource.Resources) != 1 || resource.Resources[0].Content != "resource-"+version {
			t.Fatalf("retained resource=%+v %v", resource, e)
		}
	}
	if count != 1 {
		t.Fatalf("selected skills=%d", count)
	}
	for _, m := range snapshot.Skills {
		if m.Name == "other" {
			t.Fatal("unselected sibling installed")
		}
	}
}
func (f *skillsRuntimeFixture) assertMissing(t *testing.T, pin string, id runtimecatalogcmd.SourceID) {
	t.Helper()
	for _, m := range f.snapshot(t, pin).Skills {
		if m.ID.Source == id {
			t.Fatal("excluded managed source selected")
		}
	}
}

func exerciseSkillsRecovery(t *testing.T) {
	f := newSkillsRuntimeFixture(t, false)
	f.write(t, "v1")
	candidate := f.review(t)
	before := f.inventory(t)
	f.store.fail = true
	if _, e := f.skills.Accept(t.Context(), candidate.Handle, f.authority()); !errors.Is(e, skillcmd.ErrUnavailable) {
		t.Fatalf("audit rejection=%v", e)
	}
	f.store.fail = false
	if f.inventory(t).ExpectedState != before.ExpectedState {
		t.Fatal("rejected audit changed selection")
	}
	f.gate.failPrepare = true
	if _, e := f.skills.Accept(t.Context(), candidate.Handle, f.authority()); !errors.Is(e, skillcmd.ErrUnavailable) {
		t.Fatalf("compile rejection=%v", e)
	}
	f.gate.failPrepare = false
	f.gate.failPublish = true
	out, e := f.skills.Accept(t.Context(), candidate.Handle, f.authority())
	if e != nil || !out.Accepted || !out.Pending {
		t.Fatalf("pending=%+v %v", out, e)
	}
	id := out.Entry.ID.Source
	if _, e = f.skills.Accept(t.Context(), candidate.Handle, f.authority()); !errors.Is(e, skillcmd.ErrConflict) {
		t.Fatalf("accepted handle replay=%v", e)
	}
	generation := f.inventory(t).Generation
	if e = os.RemoveAll(f.source); e != nil {
		t.Fatal(e)
	}
	f.gate.failPublish = false
	if out, e = f.skills.RetryPublication(t.Context(), f.authority()); e != nil || out.Pending || out.Generation != generation {
		t.Fatalf("retry=%+v %v", out, e)
	}
	f.change(t, skillcmd.OperationRemove, id)
	// Reopening after removing the last source must reconstruct absence, not replay install.
	f.reopen(t)
	if i := f.inventory(t); i.Pending || len(i.Entries) != 0 {
		t.Fatalf("reopen inventory=%+v", i)
	}
}
func exerciseSkillsRefresh(t *testing.T) {
	f := newSkillsRuntimeFixture(t, false)
	root := filepath.Join(f.dir, "skills", "unmanaged")
	if e := os.MkdirAll(root, 0755); e != nil {
		t.Fatal(e)
	}
	write := func(v string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: unmanaged\ndescription: Unmanaged.\n---\n"+v), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write("before")
	if out, e := f.skills.Refresh(t.Context(), f.mutation(t, runtimecatalogcmd.SourceID{})); e != nil || out.Pending {
		t.Fatalf("refresh=%+v %v", out, e)
	}
	old, e := f.catalog.Store().Application()
	if e != nil {
		t.Fatal(e)
	}
	write("after")
	_ = f.inventory(t)
	current, e := f.catalog.Store().Application()
	if e != nil || current.ID != old.ID {
		t.Fatal("inventory activated filesystem edit")
	}
	if out, e := f.skills.Refresh(t.Context(), f.mutation(t, runtimecatalogcmd.SourceID{})); e != nil || out.Pending {
		t.Fatalf("refresh=%+v %v", out, e)
	}
	current, e = f.catalog.Store().Application()
	if e != nil || current.ID == old.ID {
		t.Fatal("explicit refresh did not activate edit")
	}
}

func installSkillsFixturePlugin(t *testing.T, f *skillsRuntimeFixture) {
	t.Helper()
	root := t.TempDir()
	for name, body := range map[string]string{
		".agents/plugins/marketplace.json": `{"name":"skills-fixture","plugins":[{"name":"skills-plugin","path":"plugin"}]}`,
		"plugin/plugin.json":               `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"skills-plugin"}`,
		"plugin/skills/release/SKILL.md":   "---\nname: release\ndescription: Plugin release.\n---\nPlugin body.\n",
	} {
		p := filepath.Join(root, name)
		if e := os.MkdirAll(filepath.Dir(p), 0755); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, []byte(body), 0600); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.plugins.AddMarketplace(t.Context(), pluginapp.MarketplaceSource{Name: "skills-fixture", Source: root}); e != nil {
		t.Fatal(e)
	}
	if e := f.plugins.Install(t.Context(), "skills-plugin@skills-fixture"); e != nil {
		t.Fatal(e)
	}
}
func exerciseSkillsMixedSources(t *testing.T) {
	f := newSkillsRuntimeFixture(t, false)
	f.write(t, "v1")
	installSkillsFixturePlugin(t, f)
	c := f.review(t)
	out, e := f.skills.Accept(t.Context(), c.Handle, f.authority())
	if e != nil {
		t.Fatal(e)
	}
	i := f.inventory(t)
	var managed, plugin int
	for _, entry := range i.Entries {
		if entry.Name != "release" {
			continue
		}
		switch entry.Ownership {
		case skillcmd.OwnershipManaged:
			managed++
		case skillcmd.OwnershipPlugin:
			plugin++
			if entry.PluginID != "skills-plugin" {
				t.Fatalf("plugin source=%+v", entry)
			}
		}
	}
	if managed != 1 || plugin != 1 {
		t.Fatalf("mixed source counts=%d/%d", managed, plugin)
	}
	current, e := f.catalog.Store().Application()
	if e != nil {
		t.Fatal(e)
	}
	manager, e := baldaagent.NewSkillManager(f.catalog, f.catalog, baldaagent.SkillMetadataBudget{})
	if e != nil {
		t.Fatal(e)
	}
	bound, e := manager.BindSnapshot(t.Context(), current.ID)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = bound.Resolve(baldaagent.SkillSelector{Name: "release"}); !errors.Is(e, baldaagent.ErrSkillAmbiguous) {
		t.Fatalf("duplicate resolution=%v", e)
	}
	for _, id := range []runtimecatalogcmd.SourceID{out.Entry.ID.Source, {Kind: runtimecatalogcmd.SourceKindPlugin, Name: "skills-plugin"}} {
		selected, e := bound.Resolve(baldaagent.SkillSelector{Source: id, Name: "release"})
		if e != nil || selected.Ref.Source != id {
			t.Fatalf("qualified=%+v %v", selected, e)
		}
	}
	if _, e = f.skills.Change(t.Context(), skillcmd.OperationRemove, f.mutation(t, runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "skills-plugin"})); !errors.Is(e, skillcmd.ErrForbidden) {
		t.Fatalf("standalone plugin mutation=%v", e)
	}
}

func (f *skillsRuntimeFixture) seedHistory(t *testing.T, locator session.SessionLocator) {
	t.Helper()
	service := f.provider.RuntimeSessions()
	response, e := service.Get(t.Context(), &adksession.GetRequest{AppName: pluginsRuntimeFixtureID, UserID: "actor", SessionID: locator.SessionID})
	if e != nil {
		t.Fatal(e)
	}
	event := adksession.NewEvent(t.Context(), "skills-retained-turn")
	event.Author = "user"
	event.Actions.StateDelta = map[string]any{"retained_value": "keep"}
	if e = service.AppendEvent(t.Context(), response.Session, event); e != nil {
		t.Fatal(e)
	}
}
func (f *skillsRuntimeFixture) assertHistory(t *testing.T, locator session.SessionLocator) {
	t.Helper()
	response, e := f.provider.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: pluginsRuntimeFixtureID, UserID: "actor", SessionID: locator.SessionID})
	if e != nil {
		t.Fatal(e)
	}
	if n := response.Session.Events().Len(); n != 1 {
		t.Fatalf("retained event count=%d", n)
	}
	if v, e := response.Session.State().Get("retained_value"); e != nil || v != "keep" {
		t.Fatalf("retained state=%v %v", v, e)
	}
}
