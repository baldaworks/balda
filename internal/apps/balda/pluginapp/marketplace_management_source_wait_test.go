package pluginapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestMarketplaceManagementRevokedWhileGitFetchBlocked(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a POSIX Git upload-pack gate")
	}
	s, p, a, root := newMarketplaceManagementTestService(t)
	runGit(t, root, "init", "-b", "fixture")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "fixture")
	source := MarketplaceSource{Name: "git-market", Source: "file://" + root, Ref: "fixture"}
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: source.Name, Source: source.Source, Ref: source.Ref, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	good, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: source.Name, ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	before, err := p.PluginManagement().ObserveMarketplace(t.Context(), source.Name)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := s.marketplaceSourceSpec(source)
	if err != nil {
		t.Fatal(err)
	}
	gate := t.TempDir()
	uploadPack := filepath.Join(gate, "upload-pack")
	script := "#!/bin/sh\ngate_dir=$(dirname \"$0\")\n: > \"$gate_dir/started\"\nwhile [ ! -f \"$gate_dir/released\" ]; do sleep 0.01; done\nexec git-upload-pack \"$@\"\n"
	if err := os.WriteFile(uploadPack, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, spec.RepoRoot, "config", "remote.origin.uploadpack", uploadPack)
	release := func() {
		if err := os.WriteFile(filepath.Join(gate, "released"), nil, 0o600); err != nil {
			t.Error(err)
		}
	}
	defer release()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := s.ManagementRefreshMarketplace(ctx, plugincmd.MarketplaceMutation{Name: source.Name, ExpectedState: good.ExpectedState, Authority: a})
		result <- err
	}()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waiting := true
	for waiting {
		select {
		case err := <-result:
			t.Fatalf("refresh did not block at Git fetch: %v", err)
		case <-ctx.Done():
			t.Fatal("Git fetch gate was not reached")
		case <-ticker.C:
			if _, err := os.Stat(filepath.Join(gate, "started")); err == nil {
				waiting = false
			}
		}
	}
	now := time.Now().UTC()
	event := usercmd.AuditEvent{ID: "revoke-blocked-source", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
	if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
		t.Fatal(err)
	}
	release()
	select {
	case err := <-result:
		if !errors.Is(err, plugincmd.ErrForbidden) {
			t.Fatalf("revoked source refresh=%v", err)
		}
	case <-ctx.Done():
		t.Fatal("released Git fetch did not finish")
	}
	after, err := p.PluginManagement().ObserveMarketplace(t.Context(), source.Name)
	if err != nil || before.Token() != after.Token() {
		t.Fatal("revoked blocked fetch changed definition/discovery/generation")
	}
	audit, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	refreshed := 0
	for _, entry := range audit.Events {
		if entry.Action == usercmd.AuditActionPluginMarketplaceRefreshed {
			refreshed++
		}
		if entry.Action == usercmd.AuditActionPluginMarketplaceRefreshFailed {
			t.Fatal("revoked blocked fetch accepted failure metadata")
		}
	}
	if refreshed != 1 {
		t.Fatalf("refresh audit count=%d, want only initial success", refreshed)
	}
}
