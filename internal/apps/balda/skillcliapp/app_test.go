package skillcliapp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillapp"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
)

type fakeLifecycle struct {
	inventory      skillcmd.Inventory
	candidate      skillcmd.Candidate
	outcome        skillcmd.Outcome
	err            error
	reviewInstalls int
	reviewUpdates  int
	accepts        int
	changes        int
	refreshes      int
	recoveries     int
	lastInstall    skillapp.InstallRequest
	lastMutation   skillcmd.Mutation
	lastOperation  skillcmd.Operation
}

func (f *fakeLifecycle) Inventory(context.Context, skillcmd.Authority) (skillcmd.Inventory, error) {
	return f.inventory, f.err
}

func (f *fakeLifecycle) ReviewInstall(_ context.Context, request skillapp.InstallRequest) (skillcmd.Candidate, error) {
	f.reviewInstalls++
	f.lastInstall = request
	return f.candidate, f.err
}

func (f *fakeLifecycle) ReviewUpdate(_ context.Context, mutation skillcmd.Mutation) (skillcmd.Candidate, error) {
	f.reviewUpdates++
	f.lastMutation = mutation
	return f.candidate, f.err
}

func (f *fakeLifecycle) Accept(context.Context, string, skillcmd.Authority) (skillcmd.Outcome, error) {
	f.accepts++
	return f.outcome, f.err
}

func (f *fakeLifecycle) Change(_ context.Context, operation skillcmd.Operation, mutation skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.changes++
	f.lastOperation = operation
	f.lastMutation = mutation
	return f.outcome, f.err
}

func (f *fakeLifecycle) Refresh(_ context.Context, mutation skillcmd.Mutation) (skillcmd.Outcome, error) {
	f.refreshes++
	f.lastMutation = mutation
	return f.outcome, f.err
}

func (f *fakeLifecycle) RetryPublication(context.Context, skillcmd.Authority) (skillcmd.Outcome, error) {
	f.recoveries++
	return f.outcome, f.err
}

func managedEntry(id, name string) skillcmd.Entry {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: id}
	return skillcmd.Entry{
		ID:   runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name},
		Name: name, Ownership: skillcmd.OwnershipManaged, Enabled: true,
		Revision: "revision-public", CurrentRevision: "current-public", SourceLabel: "https://user:password-canary@example.test/private.git",
	}
}

func pluginEntry(id, name string) skillcmd.Entry {
	source := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: id}
	return skillcmd.Entry{
		ID:   runtimecatalogcmd.ContributionID{Source: source, Kind: runtimecatalogcmd.ContributionKindSkill, Name: name},
		Name: name, Ownership: skillcmd.OwnershipPlugin, Enabled: true,
	}
}

func newTestApp(t *testing.T, lifecycle *fakeLifecycle) *App {
	t.Helper()
	app, err := New(lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	app.now = func() time.Time { return time.Unix(123, 0).UTC() }
	return app
}

func TestResolveExactIDThenUniqueName(t *testing.T) {
	first := managedEntry("managed-first", "duplicate")
	second := managedEntry("managed-second", "duplicate")
	unique := pluginEntry("plugin-one", "unique")
	app := newTestApp(t, &fakeLifecycle{inventory: skillcmd.Inventory{Entries: []skillcmd.Entry{first, second, unique}}})

	entry, err := app.Show(t.Context(), "user-skill/managed-second")
	if err != nil || entry.ID != "user-skill/managed-second" {
		t.Fatalf("exact ID: entry=%+v err=%v", entry, err)
	}
	entry, err = app.Show(t.Context(), "unique")
	if err != nil || entry.ID != unique.ID.String() {
		t.Fatalf("unique name: entry=%+v err=%v", entry, err)
	}
	_, err = app.Show(t.Context(), "duplicate")
	public, ok := AsError(err)
	if !ok || public.Code != CodeAmbiguous || strings.Join(public.Candidates, ",") != "user-skill/managed-first,user-skill/managed-second" {
		t.Fatalf("ambiguity: %#v, %v", public, err)
	}
}

func TestManagedMutationRejectsOtherOwnershipBeforeConfirmation(t *testing.T) {
	fake := &fakeLifecycle{inventory: skillcmd.Inventory{ExpectedState: "state", Entries: []skillcmd.Entry{pluginEntry("plugin-one", "shared")}}}
	app := newTestApp(t, fake)
	confirmed := false
	_, err := app.Change(t.Context(), skillcmd.OperationDisable, "shared", func(Review) (bool, error) {
		confirmed = true
		return true, nil
	})
	public, ok := AsError(err)
	if !ok || public.Code != CodeNotOwned || confirmed || fake.changes != 0 {
		t.Fatalf("result: error=%v confirmed=%v changes=%d", err, confirmed, fake.changes)
	}
}

func TestInstallReviewDeclineMakesNoAcceptance(t *testing.T) {
	entry := managedEntry("managed-new", "reviewed")
	fake := &fakeLifecycle{
		inventory: skillcmd.Inventory{ExpectedState: "expected"},
		candidate: skillcmd.Candidate{Handle: "opaque", Entry: entry, SkillPath: "skills/reviewed", ResolvedCommit: "commit"},
	}
	app := newTestApp(t, fake)
	var review Review
	_, err := app.Install(t.Context(), InstallRequest{Source: "https://user:secret@example.test/private.git", Ref: "v1", Path: "skills/reviewed"}, func(in Review) (bool, error) {
		review = in
		return false, nil
	})
	public, ok := AsError(err)
	if !ok || public.Code != CodeCancelled || fake.accepts != 0 || fake.reviewInstalls != 1 {
		t.Fatalf("error=%v accepts=%d reviews=%d", err, fake.accepts, fake.reviewInstalls)
	}
	encoded, err := json.Marshal(review)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"user:secret", "password-canary", "private.git", "opaque"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("review leaked %q: %s", secret, encoded)
		}
	}
	if fake.lastInstall.Kind != "git" || fake.lastInstall.ExpectedState != "expected" || !fake.lastInstall.Authority.CLI() {
		t.Fatalf("install request: %+v", fake.lastInstall)
	}
}

func TestUpdateAcceptsReviewedCandidateAndReportsPending(t *testing.T) {
	entry := managedEntry("managed-one", "one")
	fake := &fakeLifecycle{
		inventory: skillcmd.Inventory{ExpectedState: "expected", Entries: []skillcmd.Entry{entry}},
		candidate: skillcmd.Candidate{Handle: "opaque", Entry: entry, ResolvedCommit: "commit"},
		outcome:   skillcmd.Outcome{Accepted: true, Pending: true, Generation: 7, Entry: &entry},
	}
	app := newTestApp(t, fake)
	result, err := app.Update(t.Context(), "one", func(Review) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	if !result.Accepted || result.Published || !result.Pending || !result.RestartRequired || !result.ResetRequired {
		t.Fatalf("result: %+v", result)
	}
	if fake.reviewUpdates != 1 || fake.accepts != 1 || fake.lastMutation.ID.Name != "managed-one" || !fake.lastMutation.Authority.CLI() {
		t.Fatalf("lifecycle: %+v", fake)
	}
}

func TestMissingConfirmationStopsMutation(t *testing.T) {
	entry := managedEntry("managed-one", "one")
	fake := &fakeLifecycle{inventory: skillcmd.Inventory{ExpectedState: "expected", Entries: []skillcmd.Entry{entry}}}
	app := newTestApp(t, fake)
	_, err := app.Change(t.Context(), skillcmd.OperationRemove, "one", nil)
	public, ok := AsError(err)
	if !ok || public.Code != CodeConfirmationRequired || fake.changes != 0 {
		t.Fatalf("error=%v changes=%d", err, fake.changes)
	}
}

func TestRawErrorsAreMappedToBoundedCodes(t *testing.T) {
	secret := errors.New("database password=hunter2 /protected/path")
	app := newTestApp(t, &fakeLifecycle{err: secret})
	_, err := app.List(t.Context())
	public, ok := AsError(err)
	if !ok || public.Code != CodeUnavailable || strings.Contains(public.Error(), "hunter2") || strings.Contains(public.Error(), "/protected") {
		t.Fatalf("unsafe error: %#v, %v", public, err)
	}
}

func TestInstallKind(t *testing.T) {
	tests := map[string]string{
		"/srv/skills/one":                 SourceKindLocal,
		"./skills/one":                    SourceKindLocal,
		"https://example.test/skills.git": SourceKindGit,
		"git@example.test:skills.git":     SourceKindGit,
		"example.test:skills.git":         SourceKindGit,
	}
	for source, want := range tests {
		if got := InstallKind(source); got != want {
			t.Errorf("InstallKind(%q) = %q, want %q", source, got, want)
		}
	}
}
