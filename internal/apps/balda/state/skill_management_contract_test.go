//go:build integration && (sqlite || postgres)

package state

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func checkSkillManagement(t *testing.T, open contractOpener) {
	path := filepath.Join(t.TempDir(), "skills.db")
	p, err := open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { closeContractProvider(t, p) }()
	base := contractMCPMutation(t, p).Authority
	a := skillcmd.Authority{UserID: base.UserID, UserVersion: base.UserVersion, CredentialVersion: base.CredentialVersion, MFAVersion: base.MFAVersion, SessionID: base.SessionID, SessionVersion: base.SessionVersion, At: base.At}
	store := p.SkillManagement()
	initial, err := store.Observe(t.Context())
	if err != nil || initial.Record.Generation != 0 {
		t.Fatalf("empty observation=%+v, %v", initial, err)
	}
	id := runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindUserSkill, Name: "managed-one"}
	install := skillcmd.Installation{Descriptor: runtimecatalogcmd.SourceDescriptor{ID: id, Revision: "revision-one"}, Name: "example", Description: "safe description", Origin: skillcmd.Origin{Kind: "git", Source: "https://private:credential-canary@example.test/repo", Ref: "main", Path: "skills/example"}, Enabled: true, CreatedAt: a.At, UpdatedAt: a.At}
	mutate := func(o skillcmd.Observation, records []skillcmd.Installation, operation skillcmd.Operation, audit string) skillcmd.Commit {
		return skillcmd.Commit{Authority: a, ExpectedState: o.Token(), Record: skillcmd.Record{SchemaVersion: 1, Generation: o.Record.Generation + 1, Installations: records, Pending: true, UpdatedAt: a.At}, Operation: operation, TargetID: id.Name, AuditID: audit}
	}
	m := mutate(initial, []skillcmd.Installation{install}, skillcmd.OperationInstall, "skill-install")
	if err := store.Commit(t.Context(), m); err != nil {
		t.Fatal(err)
	}
	if err := store.Commit(t.Context(), m); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("stale install=%v", err)
	}
	selection, err := store.ReadSelections(t.Context())
	if err != nil || selection.Generation != 1 || len(selection.Sources) != 1 {
		t.Fatalf("selection=%+v, %v", selection, err)
	}
	raw, _ := json.Marshal(selection)
	if strings.Contains(string(raw), "credential-canary") {
		t.Fatal("private origin leaked into catalog")
	}
	current, err := store.Observe(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	disabled := install
	disabled.Enabled = false
	duplicate := mutate(current, []skillcmd.Installation{disabled}, skillcmd.OperationDisable, "skill-install")
	if err := store.Commit(t.Context(), duplicate); !errors.Is(err, skillcmd.ErrUnavailable) {
		t.Fatalf("audit failure=%v", err)
	}
	after, _ := store.Observe(t.Context())
	if after.Token() != current.Token() {
		t.Fatal("audit failure changed aggregate")
	}
	// Concurrent administrator edits share one aggregate generation fence.
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Go(func() {
			candidate := duplicate
			candidate.AuditID = []string{"skill-disable-a", "skill-disable-b"}[i]
			errs[i] = store.Commit(t.Context(), candidate)
		})
	}
	wg.Wait()
	successes, conflicts := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, skillcmd.ErrConflict):
			conflicts++
		default:
			t.Fatalf("concurrent commit=%v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("successes=%d conflicts=%d", successes, conflicts)
	}
	if err := store.MarkPublished(t.Context(), selection); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("stale generation completion=%v", err)
	}
	empty, err := store.ReadSelections(t.Context())
	if err != nil || empty.Generation != 2 || len(empty.Sources) != 0 {
		t.Fatalf("disabled selection=%+v, %v", empty, err)
	}
	mismatched := empty
	mismatched.Sources = selection.Sources
	if err := store.MarkPublished(t.Context(), mismatched); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("mismatched set completion=%v", err)
	}
	if err := store.MarkPublished(t.Context(), empty); err != nil {
		t.Fatal(err)
	}
	closeContractProvider(t, p)
	p, err = open(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	store = p.SkillManagement()
	current, err = store.Observe(t.Context())
	if err != nil || current.Record.Pending || current.Record.Installations[0].Enabled {
		t.Fatalf("reopened=%+v, %v", current, err)
	}
	removed := current.Record.Installations[0]
	removed.Removed = true
	if err := store.Commit(t.Context(), mutate(current, []skillcmd.Installation{removed}, skillcmd.OperationRemove, "skill-remove")); err != nil {
		t.Fatal(err)
	}
	removedState, _ := store.Observe(t.Context())
	if len(removedState.Record.Installations) != 1 || !removedState.Record.Installations[0].Removed {
		t.Fatal("removed identity tombstone missing")
	}
	// A refresh can compile exactly the same empty source set but must still advance evidence.
	refreshed := mutate(removedState, removedState.Record.Installations, skillcmd.OperationRefresh, "skill-refresh")
	if err := store.Commit(t.Context(), refreshed); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPublished(t.Context(), runtimecatalogcmd.ManagedSkillSelection{Generation: 3}); !errors.Is(err, skillcmd.ErrConflict) {
		t.Fatalf("empty stale completion=%v", err)
	}
	latest, _ := store.Observe(t.Context())
	if !latest.Record.Pending {
		t.Fatal("stale completion cleared pending refresh")
	}
	events, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events.Events {
		if event.TargetType != usercmd.AuditTargetSkill {
			continue
		}
		raw, _ := json.Marshal(event)
		if strings.Contains(string(raw), "credential-canary") || event.Source != "backoffice" {
			t.Fatal("unsafe skill audit")
		}
	}
	for _, tc := range []struct {
		name   string
		change func(*skillcmd.Authority)
	}{
		{"user", func(a *skillcmd.Authority) { a.UserVersion++ }},
		{"credential", func(a *skillcmd.Authority) { a.CredentialVersion++ }},
		{"mfa", func(a *skillcmd.Authority) { a.MFAVersion++ }},
		{"session", func(a *skillcmd.Authority) { a.SessionVersion++ }},
	} {
		t.Run("authority/"+tc.name, func(t *testing.T) {
			stale := a
			tc.change(&stale)
			if err := store.CheckAuthority(t.Context(), stale); !errors.Is(err, skillcmd.ErrConflict) {
				t.Fatalf("stale authority=%v", err)
			}
			bad := mutate(latest, latest.Record.Installations, skillcmd.OperationRefresh, "skill-stale-"+tc.name)
			bad.Authority = stale
			if err := store.Commit(t.Context(), bad); !errors.Is(err, skillcmd.ErrConflict) {
				t.Fatalf("stale commit=%v", err)
			}
		})
	}
	dropped := mutate(latest, nil, skillcmd.OperationRefresh, "skill-drop-tombstone")
	if err := store.Commit(t.Context(), dropped); !errors.Is(err, skillcmd.ErrInvalid) {
		t.Fatalf("tombstone drop=%v", err)
	}
	resurrected := install
	bad := mutate(latest, []skillcmd.Installation{resurrected}, skillcmd.OperationInstall, "skill-resurrect")
	if err := store.Commit(t.Context(), bad); !errors.Is(err, skillcmd.ErrInvalid) {
		t.Fatalf("identity reuse=%v", err)
	}
	if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, a.At, "contract revocation", contractAudit("skill-revoke-session", usercmd.AuditActionSessionRevoked, a.SessionID, a.At)); err != nil {
		t.Fatal(err)
	}
	if err := store.CheckAuthority(t.Context(), a); !errors.Is(err, skillcmd.ErrForbidden) {
		t.Fatalf("revoked authority=%v", err)
	}
	revoked := mutate(latest, latest.Record.Installations, skillcmd.OperationRefresh, "skill-revoked")
	if err := store.Commit(t.Context(), revoked); !errors.Is(err, skillcmd.ErrForbidden) {
		t.Fatalf("revoked commit=%v", err)
	}
	unchanged, err := store.Observe(t.Context())
	if err != nil || unchanged.Token() != latest.Token() {
		t.Fatal("rejected mutation changed state")
	}
	if err := p.AppKV().SetJSON(t.Context(), skillcmd.ManagementKey, map[string]any{"schema_version": 99, "generation": 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReadSelections(t.Context()); !errors.Is(err, skillcmd.ErrUnavailable) {
		t.Fatalf("damaged aggregate=%v", err)
	}
}
