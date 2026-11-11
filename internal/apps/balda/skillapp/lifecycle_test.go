package skillapp_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/runtime/v2/mcpregistry"
)

type fixture struct {
	service   *skillapp.Service
	catalog   *catalogapp.Runtime
	provider  state.Provider
	authority skillcmd.Authority
	directory string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	directory := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.WalkDir(directory, func(path string, d os.DirEntry, err error) error {
			if err == nil && d.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Now().UTC()
	audit := func(id string, action usercmd.AuditAction, target usercmd.AuditTargetType, targetID string) usercmd.AuditEvent {
		return usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: target, TargetID: targetID, Source: "skill-fixture", OccurredAt: now}
	}
	user := usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "Admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Version: 1, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, CreatedAt: now, UpdatedAt: now}
	if err := provider.Users().CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: "fixture-hash"}, audit("user", usercmd.AuditActionUserCreated, usercmd.AuditTargetUser, user.ID)); err != nil {
		t.Fatal(err)
	}
	family := usercmd.SessionFamily{ID: "browser", UserID: user.ID, Version: 1, CredentialVersion: 1, Assurance: usercmd.SessionAssuranceNormal, Access: usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("digest"), ExpiresAt: now.Add(15 * time.Minute)}, CSRFVerifierDigest: []byte("csrf"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(time.Hour), RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("digest"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(time.Hour)}}}
	if err := provider.Users().CreateSession(t.Context(), family, audit("browser", usercmd.AuditActionLoginSucceeded, usercmd.AuditTargetSession, family.ID)); err != nil {
		t.Fatal(err)
	}
	catalog, err := catalogapp.NewRuntime(directory, "", "", provider, nil, nil, mcpregistry.New(nil), commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = catalog.MCP().Shutdown(t.Context()) })
	unlock, err := catalog.LockCatalogMutation(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.RetrySkillPublication(t.Context()); err != nil {
		unlock()
		t.Fatal(err)
	}
	unlock()
	service, err := skillapp.New(directory, provider.SkillManagement(), catalog)
	if err != nil {
		t.Fatal(err)
	}
	return fixture{service: service, catalog: catalog, provider: provider, directory: directory, authority: skillcmd.NewBrowserAuthority(user.ID, 1, 1, 0, family.ID, 1, now)}
}

func writeSkill(t *testing.T, root, name, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(root, "references"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "SKILL.md"), []byte("---\nname: "+name+"\ndescription: Selected skill\n---\n"+body), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "references", "details.txt"), []byte(body+" resource"), 0600); err != nil {
		t.Fatal(err)
	}
}

func (f fixture) inventory(t *testing.T) skillcmd.Inventory {
	t.Helper()
	inventory, err := f.service.Inventory(t.Context(), f.authority)
	if err != nil {
		t.Fatal(err)
	}
	return inventory
}

func nonBuiltinEntries(inventory skillcmd.Inventory) []skillcmd.Entry {
	var entries []skillcmd.Entry
	for _, entry := range inventory.Entries {
		if entry.ID.Source.Kind != runtimecatalogcmd.SourceKindBuiltin {
			entries = append(entries, entry)
		}
	}
	return entries
}

func nonBuiltinSkills(snapshot runtimecatalogcmd.Snapshot) []runtimecatalogcmd.SkillMetadata {
	var skills []runtimecatalogcmd.SkillMetadata
	for _, skill := range snapshot.Skills {
		if skill.ID.Source.Kind != runtimecatalogcmd.SourceKindBuiltin {
			skills = append(skills, skill)
		}
	}
	return skills
}

func (f fixture) mutation(t *testing.T, id runtimecatalogcmd.SourceID) skillcmd.Mutation {
	t.Helper()
	return skillcmd.Mutation{ID: id, ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority}
}
func (f fixture) install(t *testing.T, source, path string) skillcmd.Candidate {
	t.Helper()
	candidate, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: source, Path: path, ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority})
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := f.service.Accept(t.Context(), candidate.Handle, f.authority)
	if err != nil || !outcome.Accepted || outcome.Pending {
		t.Fatalf("accept=%+v,%v", outcome, err)
	}
	return candidate
}

func TestLocalLifecyclePreservesArchivedContentAndDisabledUpdate(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "picked"), "selected", "original")
	writeSkill(t, filepath.Join(root, "sibling"), "sibling", "not installed")
	candidate := f.install(t, root, "picked")
	id := candidate.Entry.ID.Source
	pinned, err := f.catalog.CurrentSkillSnapshot(t.Context(), "")
	if err != nil {
		t.Fatal(err)
	}
	if skills := nonBuiltinSkills(pinned); len(skills) != 1 || skills[0].Name != "selected" {
		t.Fatalf("installed siblings: %+v", pinned.Skills)
	}
	if _, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: root, Path: "picked", ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority}); err != skillcmd.ErrConflict {
		t.Fatalf("duplicate origin=%v", err)
	}
	if _, err := f.service.Change(t.Context(), skillcmd.OperationDisable, f.mutation(t, id)); err != nil {
		t.Fatal(err)
	}
	writeSkill(t, filepath.Join(root, "picked"), "selected", "updated")
	update, err := f.service.ReviewUpdate(t.Context(), f.mutation(t, id))
	if err != nil {
		t.Fatal(err)
	}
	if update.Entry.Enabled {
		t.Fatal("disabled review became enabled")
	}
	if outcome, err := f.service.Accept(t.Context(), update.Handle, f.authority); err != nil || outcome.Pending {
		t.Fatalf("update=%+v,%v", outcome, err)
	}
	inventory := f.inventory(t)
	entries := nonBuiltinEntries(inventory)
	if len(entries) != 1 || entries[0].Enabled || entries[0].Revision == candidate.Entry.Revision {
		t.Fatalf("disabled updated inventory=%+v", inventory)
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Change(t.Context(), skillcmd.OperationEnable, f.mutation(t, id)); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Change(t.Context(), skillcmd.OperationRemove, f.mutation(t, id)); err != nil {
		t.Fatal(err)
	}
	if len(nonBuiltinEntries(f.inventory(t))) != 0 {
		t.Fatal("removed installation visible")
	}
	loaded, err := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: runtimecatalogcmd.SkillRef{Source: id, Revision: candidate.Entry.Revision, Name: "selected"}, MainResource: "selected/SKILL.md", Resources: []string{"references/details.txt"}})
	if err != nil || !strings.Contains(loaded.Instructions, "original") || len(loaded.Resources) != 1 || loaded.Resources[0].Content != "original resource" {
		t.Fatalf("retained content=%+v,%v", loaded, err)
	}
	audit, err := f.provider.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(audit)
	if strings.Contains(string(raw), root) || strings.Contains(string(raw), "original") {
		t.Fatalf("audit disclosed source/body: %s", raw)
	}
}

func TestReviewRejectsChangedContentStateIdentityAndAuthority(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	writeSkill(t, root, "selected", "first")
	request := skillapp.InstallRequest{Source: root, Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState}
	candidate, err := f.service.ReviewInstall(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	foreign := f.authority
	foreign.SessionID = "other"
	if _, err := f.service.Accept(t.Context(), candidate.Handle, foreign); err != skillcmd.ErrForbidden {
		t.Fatalf("foreign candidate=%v", err)
	}
	writeSkill(t, root, "selected", "changed")
	if _, err := f.service.Accept(t.Context(), candidate.Handle, f.authority); err != skillcmd.ErrConflict {
		t.Fatalf("changed source=%v", err)
	}
	if len(nonBuiltinEntries(f.inventory(t))) != 0 {
		t.Fatal("changed candidate accepted")
	}
	fresh, err := f.service.ReviewInstall(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Refresh(t.Context(), skillcmd.Mutation{Authority: f.authority, ExpectedState: request.ExpectedState}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Accept(t.Context(), fresh.Handle, f.authority); err != skillcmd.ErrConflict {
		t.Fatalf("stale candidate=%v", err)
	}
	current := f.install(t, root, ".")
	writeSkill(t, root, "renamed", "name changed")
	if _, err := f.service.ReviewUpdate(t.Context(), f.mutation(t, current.Entry.ID.Source)); err != skillcmd.ErrConflict {
		t.Fatalf("name drift=%v", err)
	}
	for _, id := range []runtimecatalogcmd.SourceID{{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "plugin"}, {Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "default"}} {
		if _, err := f.service.Change(t.Context(), skillcmd.OperationRemove, f.mutation(t, id)); err != skillcmd.ErrForbidden {
			t.Fatalf("nonmanaged change=%v", err)
		}
	}
}

func TestGitReviewSelectsSubtreeAndRejectsMovedRef(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	writeSkill(t, filepath.Join(root, "first"), "chosen", "first revision")
	writeSkill(t, filepath.Join(root, "second"), "other", "other")
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_CONFIG_GLOBAL=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v %s", args, err, output)
		}
	}
	git("init", "--initial-branch=main")
	git("add", ".")
	git("commit", "-qm", "initial")
	git("branch", "feature")
	request := skillapp.InstallRequest{Source: "file://" + root, Ref: "feature", Path: "first", ExpectedState: f.inventory(t).ExpectedState, Authority: f.authority}
	candidate, err := f.service.ReviewInstall(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.ResolvedCommit == "" || candidate.Entry.Name != "chosen" {
		t.Fatalf("Git review=%+v", candidate)
	}
	git("checkout", "--quiet", "feature")
	writeSkill(t, filepath.Join(root, "first"), "chosen", "new revision")
	git("add", ".")
	git("commit", "-qm", "update")
	if _, err := f.service.Accept(t.Context(), candidate.Handle, f.authority); err != skillcmd.ErrConflict {
		t.Fatalf("moved ref=%v", err)
	}
	fresh, err := f.service.ReviewInstall(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if outcome, err := f.service.Accept(t.Context(), fresh.Handle, f.authority); err != nil || outcome.Pending {
		t.Fatalf("Git accept=%+v,%v", outcome, err)
	}
	if len(nonBuiltinEntries(f.inventory(t))) != 1 {
		t.Fatal("Git sibling installed")
	}
	request.Ref = "main"
	request.ExpectedState = f.inventory(t).ExpectedState
	if _, err := f.service.ReviewInstall(t.Context(), request); err != skillcmd.ErrConflict {
		t.Fatalf("same source/path at another ref=%v", err)
	}
}

type catalogFault struct {
	skillapp.Catalog
	prepareErr   error
	publishErr   error
	captureCount int
	gateHook     func()
}

func (c *catalogFault) CaptureCandidate(ctx context.Context, root *os.Root, path string, id runtimecatalogcmd.SourceID) (runtimecatalogcmd.Source, error) {
	c.captureCount++
	return c.Catalog.CaptureCandidate(ctx, root, path, id)
}
func (c *catalogFault) PrepareSkillCandidate(ctx context.Context, selection runtimecatalogcmd.ManagedSkillSelection) (runtimecatalogcmd.PreparedApplicationCandidate, error) {
	if c.prepareErr != nil {
		return runtimecatalogcmd.PreparedApplicationCandidate{}, c.prepareErr
	}
	return c.Catalog.PrepareSkillCandidate(ctx, selection)
}
func (c *catalogFault) PublishCandidate(ctx context.Context, candidate runtimecatalogcmd.PreparedApplicationCandidate) error {
	if c.publishErr != nil {
		return c.publishErr
	}
	return c.Catalog.PublishCandidate(ctx, candidate)
}
func (c *catalogFault) LockCatalogMutation(ctx context.Context) (func(), error) {
	unlock, err := c.Catalog.LockCatalogMutation(ctx)
	if err == nil && c.gateHook != nil {
		c.gateHook()
	}
	return unlock, err
}

func TestPublicationFaultIsAcceptedPendingAndRetryDoesNotFetchOrAuditAgain(t *testing.T) {
	f := newFixture(t)
	fault := &catalogFault{Catalog: f.catalog, publishErr: errors.New("source-secret-canary")}
	service, err := skillapp.New(f.directory, f.provider.SkillManagement(), fault)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	root := t.TempDir()
	writeSkill(t, root, "selected", "original")
	candidate, err := service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: root, Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState})
	if err != nil {
		t.Fatal(err)
	}
	fault.prepareErr = errors.New("compile failed")
	if outcome, err := service.Accept(t.Context(), candidate.Handle, f.authority); err != skillcmd.ErrUnavailable || outcome.Accepted {
		t.Fatalf("precommit failure=%+v,%v", outcome, err)
	}
	fault.prepareErr = nil
	outcome, err := service.Accept(t.Context(), candidate.Handle, f.authority)
	if err != nil || !outcome.Accepted || !outcome.Pending {
		t.Fatalf("postcommit failure=%+v,%v", outcome, err)
	}
	inventory := f.inventory(t)
	entries := nonBuiltinEntries(inventory)
	if !inventory.Pending || len(entries) != 1 || !entries[0].Pending || entries[0].CurrentRevision != "" {
		t.Fatalf("pending inventory=%+v", inventory)
	}
	if _, err := service.Accept(t.Context(), candidate.Handle, f.authority); err != skillcmd.ErrConflict {
		t.Fatalf("accepted replay=%v", err)
	}
	auditsBefore, err := f.provider.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	count := fault.captureCount
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	outcome, err = service.RetryPublication(t.Context(), f.authority)
	if err != nil || outcome.Pending {
		t.Fatalf("retry=%+v,%v", outcome, err)
	}
	auditsAfter, err := f.provider.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	if fault.captureCount != count || len(auditsBefore.Events) != len(auditsAfter.Events) {
		t.Fatal("recovery fetched origin or duplicated audit")
	}
}

func TestInventoryDoesNotFetchAndExplicitRefreshAdoptsUnmanagedSkill(t *testing.T) {
	f := newFixture(t)
	original, _ := f.catalog.CurrentSkillSnapshot(t.Context(), "")
	writeSkill(t, filepath.Join(f.directory, "skills", "unmanaged"), "unmanaged", "body")
	before := f.inventory(t)
	still, _ := f.catalog.CurrentSkillSnapshot(t.Context(), "")
	if original.ID != still.ID || len(nonBuiltinEntries(before)) != 0 {
		t.Fatal("inventory activated filesystem change")
	}
	if _, err := f.service.Refresh(t.Context(), skillcmd.Mutation{ExpectedState: before.ExpectedState, Authority: f.authority}); err != nil {
		t.Fatal(err)
	}
	if inventory := f.inventory(t); len(nonBuiltinEntries(inventory)) != 1 || nonBuiltinEntries(inventory)[0].Ownership != skillcmd.OwnershipUnmanaged {
		t.Fatalf("refreshed=%+v", inventory)
	}
	fault := &catalogFault{Catalog: f.catalog, prepareErr: errors.New("sensitive filesystem path")}
	service, err := skillapp.New(f.directory, f.provider.SkillManagement(), fault)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	before = f.inventory(t)
	if _, err := service.Refresh(t.Context(), skillcmd.Mutation{ExpectedState: before.ExpectedState, Authority: f.authority}); err != skillcmd.ErrUnavailable {
		t.Fatalf("refresh failure=%v", err)
	}
	after := f.inventory(t)
	if after.ExpectedState != before.ExpectedState || len(nonBuiltinEntries(after)) != 1 || len(after.Diagnostics) == 0 {
		t.Fatalf("refresh failure state=%+v", after)
	}
}

func TestAcceptRechecksAuthorityAfterCatalogGate(t *testing.T) {
	f := newFixture(t)
	fault := &catalogFault{Catalog: f.catalog}
	service, err := skillapp.New(f.directory, f.provider.SkillManagement(), fault)
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	root := t.TempDir()
	writeSkill(t, root, "selected", "original")
	candidate, err := service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: root, Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState})
	if err != nil {
		t.Fatal(err)
	}
	fault.gateHook = func() {
		now := time.Now().UTC()
		err := f.provider.Users().RevokeSession(t.Context(), f.authority.SessionID, 1, now, "revoked", usercmd.AuditEvent{ID: "revoke", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: f.authority.SessionID, Source: "fixture", OccurredAt: now})
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Accept(t.Context(), candidate.Handle, f.authority); err != skillcmd.ErrForbidden {
		t.Fatalf("revoked authority=%v", err)
	}
	observation, err := f.provider.SkillManagement().Observe(t.Context())
	if err != nil || observation.Record.Generation != 0 {
		t.Fatalf("revoked mutation wrote state=%+v,%v", observation, err)
	}
}

func TestRootGitSkillRetainsOnlySkillResourcesAtStableRevision(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	writeSkill(t, root, "root-skill", "root instructions")
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = root
		command.Env = append(os.Environ(), "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.test", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.test", "GIT_CONFIG_GLOBAL=/dev/null")
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git: %v %s", err, output)
		}
	}
	git("init", "--initial-branch=main")
	git("add", ".")
	git("commit", "-qm", "root skill")
	candidate, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: "file://" + root, Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState})
	if err != nil {
		t.Fatal(err)
	}
	if candidate.SkillPath != "." || candidate.Entry.Name != "root-skill" {
		t.Fatalf("root review=%+v", candidate)
	}
	// A separate clone at acceptance must produce the same skill content digest.
	outcome, err := f.service.Accept(t.Context(), candidate.Handle, f.authority)
	if err != nil || outcome.Pending {
		t.Fatalf("root accept=%+v,%v", outcome, err)
	}
	ref := runtimecatalogcmd.SkillRef{Source: candidate.Entry.ID.Source, Revision: candidate.Entry.Revision, Name: "root-skill"}
	loaded, err := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: ref, MainResource: "root-skill/SKILL.md", Resources: []string{"references/details.txt"}})
	if err != nil || len(loaded.Resources) != 1 || loaded.Resources[0].Content != "root instructions resource" {
		t.Fatalf("root resources=%+v,%v", loaded, err)
	}
	if _, err := f.catalog.ReadSkill(t.Context(), runtimecatalogcmd.SkillReadRequest{Ref: ref, MainResource: "root-skill/SKILL.md", Resources: []string{".git/config"}}); err == nil {
		t.Fatal("private materialization metadata became skill content")
	}
}

func TestReviewAuthorityBeforeSourceAccessAndContainment(t *testing.T) {
	f := newFixture(t)
	_, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: "/missing/private/source", ExpectedState: f.inventory(t).ExpectedState})
	if err != skillcmd.ErrForbidden {
		t.Fatalf("unauthorized filesystem probe=%v", err)
	}
	root := t.TempDir()
	outside := t.TempDir()
	writeSkill(t, outside, "selected", "outside content")
	if err := os.Symlink(outside, filepath.Join(root, "selected")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: root, Path: "selected", Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState}); err == nil {
		t.Fatal("escaping selected subtree accepted")
	}
	writeSkill(t, filepath.Join(root, "malformed"), "valid", "body")
	if err := os.WriteFile(filepath.Join(root, "malformed", "SKILL.md"), []byte("not frontmatter"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ReviewInstall(t.Context(), skillapp.InstallRequest{Source: root, Path: "malformed", Authority: f.authority, ExpectedState: f.inventory(t).ExpectedState}); err != skillcmd.ErrInvalid {
		t.Fatalf("malformed skill=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := f.service.ReviewInstall(ctx, skillapp.InstallRequest{Source: root, Authority: f.authority}); err == nil {
		t.Fatal("canceled review succeeded")
	}
}
