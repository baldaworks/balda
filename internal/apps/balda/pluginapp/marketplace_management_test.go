package pluginapp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const managementTestVersion = "1.0.0"

func newMarketplaceManagementTestService(t *testing.T) (*Service, state.Provider, plugincmd.Authority, string) {
	t.Helper()
	dir := t.TempDir()
	p, err := state.NewSQLiteProvider(t.Context(), filepath.Join(dir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	now := time.Now().UTC()
	u := usercmd.User{ID: "admin", DisplayName: "Admin", Username: "admin", NormalizedUsername: "admin", Status: usercmd.StatusActive, Role: usercmd.RoleAdministrator, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now}
	audit := usercmd.AuditEvent{ID: "create-admin", Action: usercmd.AuditActionUserCreated, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: usercmd.AuditTargetUser, TargetID: u.ID, Source: "test", OccurredAt: now}
	if err := p.Users().CreateUser(t.Context(), u, usercmd.CredentialSecret{UserID: u.ID, PasswordHash: "hash"}, audit); err != nil {
		t.Fatal(err)
	}
	f := usercmd.SessionFamily{ID: "browser", UserID: u.ID, Assurance: usercmd.SessionAssuranceNormal, CredentialVersion: 1, Access: usercmd.AccessCredential{Selector: "access", VerifierDigest: []byte("access"), ExpiresAt: now.Add(time.Hour)}, CSRFVerifierDigest: []byte("csrf"), CreatedAt: now, LastSeenAt: now, RefreshExpiresAt: now.Add(24 * time.Hour), Version: 1, RefreshTokens: []usercmd.RefreshToken{{Selector: "refresh", VerifierDigest: []byte("refresh"), Generation: 1, State: usercmd.RefreshTokenStateActive, IssuedAt: now, ExpiresAt: now.Add(24 * time.Hour)}}}
	audit.ID, audit.Action, audit.TargetID = "login", usercmd.AuditActionLoginSucceeded, f.ID
	if err := p.Users().CreateSession(t.Context(), f, audit); err != nil {
		t.Fatal(err)
	}
	s, err := NewManaged(dir, p.AppKV(), p.Plugins(), &recordingActivator{}, p.PluginManagement())
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writeManagedPlugin(t, root, managementTestVersion, false)
	mustMkdirAll(t, filepath.Join(root, ".agents", "plugins"))
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"name":"index-identity","plugins":[{"name":"demo","path":"plugins/demo"}]}`)
	return s, p, plugincmd.Authority{UserID: u.ID, UserVersion: 1, CredentialVersion: 1, SessionID: f.ID, SessionVersion: 1, At: now}, root
}

func TestMarketplaceManagementAcceptedDiscovery(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if added.Source != "local" || added.LastAttemptStatus != "" {
		t.Fatalf("unexpected initial safe view: %+v", added)
	}
	if _, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a}); !errors.Is(err, plugincmd.ErrConflict) {
		t.Fatalf("duplicate add: %v", err)
	}
	refreshed, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: "market", ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshed.Candidates) != 1 || refreshed.Candidates[0].Marketplace != "market" || refreshed.Candidates[0].CandidateToken == "" {
		t.Fatalf("accepted candidates: %+v", refreshed)
	}
}

func TestMarketplaceManagementGitRefreshAndCacheIdentity(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	runGit(t, root, "init", "-b", "fixture")
	runGit(t, root, "config", "user.name", "Test")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "fixture one")
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "git-market", Source: "file://" + root, Ref: "fixture", Sparse: []string{".agents/plugins", "plugins"}, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: added.Name, ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Candidates) != 1 || first.Candidates[0].Version != managementTestVersion || first.Candidates[0].CandidateToken == "" {
		t.Fatalf("git discovery: %+v", first)
	}
	writeManagedPlugin(t, root, "2.0.0", false)
	runGit(t, root, "add", ".")
	runGit(t, root, "commit", "-m", "fixture two")
	unchanged, err := s.ManagementMarketplace(t.Context(), added.Name, a)
	if err != nil {
		t.Fatal(err)
	}
	if unchanged.Candidates[0].Version != managementTestVersion {
		t.Fatal("GET fetched unaccepted source")
	}
	second, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: added.Name, ExpectedState: first.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if second.Candidates[0].Version != "2.0.0" || second.ResolvedRevision == first.ResolvedRevision {
		t.Fatalf("refresh did not advance branch: %+v", second)
	}
	// Chat definition replacement must not refresh the former cached Git origin.
	replacement := t.TempDir()
	writeManagedPlugin(t, replacement, "3.0.0", false)
	mustMkdirAll(t, filepath.Join(replacement, ".agents", "plugins"))
	mustWriteFile(t, filepath.Join(replacement, marketplaceManifestPath), `{"plugins":[{"name":"demo","path":"plugins/demo"}]}`)
	runGit(t, replacement, "init", "-b", "fixture")
	runGit(t, replacement, "config", "user.name", "Test")
	runGit(t, replacement, "config", "user.email", "test@example.com")
	runGit(t, replacement, "add", ".")
	runGit(t, replacement, "commit", "-m", "replacement")
	if err := s.AddMarketplace(t.Context(), MarketplaceSource{Name: added.Name, Source: "file://" + replacement, Ref: "fixture"}); err != nil {
		t.Fatal(err)
	}
	cleared, err := s.ManagementMarketplace(t.Context(), added.Name, a)
	if err != nil {
		t.Fatal(err)
	}
	if cleared.ExpectedState == second.ExpectedState || len(cleared.Candidates) != 0 {
		t.Fatal("chat replacement retained actionable discovery")
	}
	if _, err := s.UpgradeMarketplaces(t.Context(), added.Name); err != nil {
		t.Fatal(err)
	}
	current, err := s.ManagementMarketplace(t.Context(), added.Name, a)
	if err != nil {
		t.Fatal(err)
	}
	if current.Candidates[0].Version != "3.0.0" {
		t.Fatal("replacement used prior cache origin")
	}
}

func TestMarketplaceManagementSourceValidationAndProjection(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	for _, r := range []plugincmd.MarketplaceAdd{
		{Name: "../escape", Source: root},
		{Name: "option-ref", Source: "https://example.com/repo.git", Ref: "--upload-pack=canary"},
		{Name: "escape-sparse", Source: "https://example.com/repo.git", Sparse: []string{"../escape"}},
		{Name: "local-params", Source: root, Ref: "ignored"},
	} {
		r.Authority = a
		if _, err := s.ManagementAddMarketplace(t.Context(), r); !errors.Is(err, plugincmd.ErrInvalid) {
			t.Fatalf("invalid definition %s: %v", r.Name, err)
		}
	}
	for _, tc := range []struct{ name, source, want string }{
		{"relative", "./private/local-source", "local"},
		{"https", "https://user:private-password@example.com/repo.git?secret=private-query#private-fragment", "https://example.com/repo.git"},
		{"scp", "alice@example.com:org/repo.git", "ssh://example.com/org/repo.git"},
	} {
		detail, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: tc.name, Source: tc.source, Authority: a})
		if err != nil {
			t.Fatal(err)
		}
		if detail.Source != tc.want {
			t.Fatalf("safe %s source=%q,want %q", tc.name, detail.Source, tc.want)
		}
	}
}

func TestMarketplaceManagementInvalidPackageIsNotActionable(t *testing.T) {
	s, _, a, root := newMarketplaceManagementTestService(t)
	mustMkdirAll(t, filepath.Join(root, "plugins", "demo", ".plugin"))
	mustWriteFile(t, filepath.Join(root, "plugins", "demo", ".plugin", "plugin.json"), `{"$schema":"https://agent-plugins.org/schemas/1.0.0/plugin.schema.json","name":"demo","version":"1.0.0"}`)
	if err := os.Remove(filepath.Join(root, "plugins", "demo", "plugin.json")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(root, marketplaceManifestPath), `{"plugins":[{"name":"demo","path":"plugins/demo","manifest_path":".plugin/plugin.json"}]}`)
	added, err := s.ManagementAddMarketplace(t.Context(), plugincmd.MarketplaceAdd{Name: "market", Source: root, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	detail, err := s.ManagementRefreshMarketplace(t.Context(), plugincmd.MarketplaceMutation{Name: added.Name, ExpectedState: added.ExpectedState, Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Candidates) != 1 || detail.Candidates[0].CandidateToken != "" || len(detail.Candidates[0].DiagnosticCodes) == 0 {
		t.Fatalf("invalid package actionable: %+v", detail)
	}
}
