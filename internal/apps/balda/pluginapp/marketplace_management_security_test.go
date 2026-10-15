package pluginapp

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func TestMarketplaceDiscoveryGitLogsArePrivate(t *testing.T) {
	s, _, a, _ := newMarketplaceManagementTestService(t)
	var recorded bytes.Buffer
	previous := log.Logger
	log.Logger = zerolog.New(&recorded)
	t.Cleanup(func() { log.Logger = previous })
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "private-git", Source: "https://username:secret-canary@127.0.0.1:1/repo?token=query-canary", Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	failed, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "private-git", ExpectedState: added.ExpectedState, Authority: a})
	if !errors.Is(err, plugincmd.ErrUnavailable) || failed.LastAttemptStatus != "failed" {
		t.Fatalf("failed Git refresh=%v", err)
	}
	for _, private := range []string{"username", "secret-canary", "query-canary", s.stateDir} {
		if strings.Contains(recorded.String(), private) {
			t.Errorf("Git log disclosed private source field %q", private)
		}
	}
}

func TestMarketplaceDiscoverySafeSource(t *testing.T) {
	for _, source := range []string{"/private/local-source", "./private/relative-source"} {
		if got := safeMarketplaceSource(source, marketplaceSourceKindLocal); got != "local" {
			t.Errorf("local source projection=%q", got)
		}
	}
	for _, source := range []string{"https://username:secret-canary@example.test/repo?token=query-canary#fragment-canary", "ssh://username:secret-canary@example.test/repo", "username@example.test:repo?token=query-canary#fragment-canary"} {
		got := safeMarketplaceSource(source, marketplaceSourceKindGit)
		for _, private := range []string{"username", "secret-canary", "query-canary", "fragment-canary"} {
			if strings.Contains(got, private) {
				t.Errorf("source projection disclosed %s in %q", private, got)
			}
		}
	}
}

type marketplaceAcceptanceHook struct {
	state.PluginManagementStore
	before func(*state.MarketplaceCommit)
}

func (s marketplaceAcceptanceHook) CommitMarketplace(ctx context.Context, m state.MarketplaceCommit) error {
	s.before(&m)
	return s.PluginManagementStore.CommitMarketplace(ctx, m)
}

func TestMarketplaceManagementRejectsLateAuthorityAndAuditFailure(t *testing.T) {
	for _, failure := range []string{"audit", "revoked"} {
		t.Run(failure, func(t *testing.T) {
			s, p, a, root := newMarketplaceManagementTestService(t)
			added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
			if err != nil {
				t.Fatal(err)
			}
			good, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a})
			if err != nil {
				t.Fatal(err)
			}
			before, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
			if err != nil {
				t.Fatal(err)
			}
			// A genuine failed discovery reaches the same guarded commit boundary.
			mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"plugins":"invalid index"}`)
			var reached bool
			hook := marketplaceAcceptanceHook{PluginManagementStore: p.PluginManagement(), before: func(m *state.MarketplaceCommit) {
				reached = true
				if failure == "audit" {
					m.AuditID = managementFixtureLoginAuditID
					return
				}
				now := time.Now().UTC()
				event := usercmd.AuditEvent{ID: "revoke-at-acceptance", Action: usercmd.AuditActionSessionRevoked, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetSession, TargetID: a.SessionID, Source: "test", OccurredAt: now}
				if err := p.Users().RevokeSession(t.Context(), a.SessionID, a.SessionVersion, now, "test", event); err != nil {
					t.Fatal(err)
				}
			}}
			s, err = NewManaged(s.stateDir, p.AppKV(), p.Plugins(), &recordingActivator{}, hook)
			if err != nil {
				t.Fatal(err)
			}
			_, err = s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: good.ExpectedState, Authority: a})
			want := plugincmd.ErrUnavailable
			if failure == "revoked" {
				want = plugincmd.ErrForbidden
			}
			if !reached || !errors.Is(err, want) {
				t.Fatalf("late %s reached=%v error=%v", failure, reached, err)
			}
			after, err := p.PluginManagement().ObserveMarketplace(t.Context(), "market")
			if err != nil {
				t.Fatal(err)
			}
			if after.Token() != before.Token() {
				t.Fatal("rejected failure observation changed accepted discovery or generation")
			}
			page, err := p.Users().ListAuditEvents(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
			if err != nil {
				t.Fatal(err)
			}
			for _, event := range page.Events {
				if event.Action == usercmd.AuditActionPluginMarketplaceRefreshFailed {
					t.Fatal("unauthorized or unaudited failure observation was accepted")
				}
			}
		})
	}
}
