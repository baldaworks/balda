package main

import (
	"bytes"
	"database/sql"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

func TestBackofficeBootstrapUsesAutomaticallyUpgradedState(t *testing.T) {
	fixture := readUserUpgradeFixture(t)
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	configPath := filepath.Join(workingDir, ".config", "balda", "config.yaml")
	if err := writeFile(configPath, `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  state_dir: .config/balda
`); err != nil {
		t.Fatal(err)
	}
	database := state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(workingDir, ".config", "balda", "state.db")}}
	seedUserUpgradeDatabase(t, database.SQLite.Path, fixture)
	provider, err := state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC)
	page, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	var primary usercmd.User
	for _, user := range page.Users {
		if user.Primary {
			primary = user
		}
	}
	if primary.ID == "" {
		t.Fatal("migration produced no primary administrator")
	}
	if primary.Credential.State != usercmd.CredentialStateDisabled {
		t.Fatalf("converted credential = %q", primary.Credential.State)
	}
	if users.BotCapability(primary) != usercmd.BotCapabilityOwner {
		t.Fatal("converted owner cannot use bot authorization before browser bootstrap")
	}
	bindingID := primary.Binding.ID
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	setBaldaAdminPasswordGenerator(t, "first-generated-administrator-password")
	initialBootstrap, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	initialBootstrap.SetIn(strings.NewReader(""))
	initialBootstrap.SetOut(&bytes.Buffer{})
	initialBootstrap.SetErr(&bytes.Buffer{})
	initialBootstrap.SetArgs([]string{"backoffice", "bootstrap-admin"})
	if err := initialBootstrap.Execute(); err != nil {
		t.Fatal(err)
	}
	provider, err = state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapped, found, err := provider.Users().GetUser(t.Context(), primary.ID)
	if err != nil || !found || bootstrapped.Credential.State != usercmd.CredentialStateActive {
		t.Fatalf("bootstrapped user = %+v, found=%t, err=%v", bootstrapped, found, err)
	}
	primary = bootstrapped
	familyCreatedAt := registeredAt.Add(time.Hour)
	refreshExpiry := familyCreatedAt.Add(12 * time.Hour)
	family := usercmd.SessionFamily{
		ID: "pre-bootstrap-family", UserID: primary.ID, Assurance: usercmd.SessionAssuranceRestricted,
		CredentialVersion:  primary.Credential.Version,
		Access:             usercmd.AccessCredential{Selector: "pre-bootstrap-access", VerifierDigest: []byte("access-digest"), ExpiresAt: familyCreatedAt.Add(15 * time.Minute)},
		CSRFVerifierDigest: []byte("csrf-digest"), CreatedAt: familyCreatedAt, LastSeenAt: familyCreatedAt,
		RefreshExpiresAt: refreshExpiry, Version: 1,
		RefreshTokens: []usercmd.RefreshToken{{
			Selector: "pre-bootstrap-refresh", VerifierDigest: []byte("refresh-digest"), Generation: 1,
			State: usercmd.RefreshTokenStateActive, IssuedAt: familyCreatedAt, ExpiresAt: refreshExpiry,
		}},
	}
	if err := provider.Users().CreateSession(t.Context(), family, usercmd.AuditEvent{
		ID: "audit-pre-bootstrap-login", Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: usercmd.AuditTargetSession, TargetID: family.ID, Source: "backoffice-test", OccurredAt: familyCreatedAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	bootstrapOutput := &bytes.Buffer{}
	setBaldaAdminPasswordGenerator(t, "generated-admin-password-for-migration")
	bootstrap, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.SetIn(strings.NewReader(""))
	bootstrap.SetOut(bootstrapOutput)
	bootstrap.SetErr(&bytes.Buffer{})
	bootstrap.SetArgs([]string{"backoffice", "bootstrap-admin", "--reset"})
	if err := bootstrap.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bootstrapOutput.String(), "administrator password: generated-admin-password-for-migration") {
		t.Fatalf("bootstrap output missing generated reset password: %q", bootstrapOutput.String())
	}
	provider, err = state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	page, err = provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Users) != 2 {
		t.Fatalf("canonical users = %d, want 2", len(page.Users))
	}
	owner, found, err := provider.Users().GetUserByBinding(t.Context(), "telegram", "101")
	if err != nil || !found || users.BotCapability(owner) != usercmd.BotCapabilityOwner {
		t.Fatalf("owner binding = %+v, found=%t, error=%v", owner, found, err)
	}
	if owner.ID != primary.ID || owner.Binding.ID != bindingID {
		t.Fatal("credential bootstrap/reset replaced owner identity or binding")
	}
	collaborator, found, err := provider.Users().GetUserByBinding(t.Context(), "telegram", "202")
	if err != nil || !found || users.BotCapability(collaborator) != usercmd.BotCapabilityCollaborator {
		t.Fatalf("collaborator binding = %+v, found=%t, error=%v", collaborator, found, err)
	}
	for _, user := range page.Users {
		if user.Primary && user.Credential.State != usercmd.CredentialStateActive {
			t.Fatalf("primary credential state = %q, want active", user.Credential.State)
		}
		if user.Primary {
			secret, found, err := provider.Users().GetCredentialSecret(t.Context(), user.ID)
			if err != nil || !found || !userpassword.Verify(secret.PasswordHash, []byte("generated-admin-password-for-migration")) {
				t.Fatalf("reset credential verification: found=%t error=%v", found, err)
			}
		}
	}
	revoked, found, err := provider.Users().GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || revoked.Family.RevokedAt.IsZero() {
		t.Fatalf("session after bootstrap reset = %+v, found=%t, error=%v", revoked, found, err)
	}
	if len(revoked.Family.RefreshTokens) != 1 || revoked.Family.RefreshTokens[0].State != usercmd.RefreshTokenStateRevoked {
		t.Fatalf("refresh lineage after bootstrap reset = %+v", revoked.Family.RefreshTokens)
	}
}

func TestReadBackofficePassword(t *testing.T) {
	t.Parallel()
	password, err := readBackofficePassword(strings.NewReader("correct horse battery staple\n"))
	if err != nil || string(password) != "correct horse battery staple" {
		t.Fatalf("readBackofficePassword() = %q, %v", password, err)
	}
	zeroPassword(password)
	if _, err := readBackofficePassword(strings.NewReader("short\n")); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestBackofficeBootstrapCreatesSelectedDatabase(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	configPath := filepath.Join(workingDir, ".config", "balda", "config.yaml")
	if err := writeFile(configPath, `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  telegram:
    token: test-token
  state_dir: .config/balda
`); err != nil {
		t.Fatal(err)
	}
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	command.SetIn(strings.NewReader("correct horse battery staple\n"))
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"backoffice", "bootstrap-admin", "--username", usercmd.PrimaryUsername})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	provider, err := state.Open(t.Context(), state.DatabaseConfig{
		Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(workingDir, ".config", "balda", "state.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	page, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: 1})
	if err != nil || len(page.Users) != 1 || !page.Users[0].Primary || page.Users[0].DisplayName != usercmd.PrimaryUsername {
		t.Fatalf("fresh administrator = %+v, error = %v", page, err)
	}
}

func TestBackofficeBootstrapGeneratesPasswordWithoutInput(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  state_dir: .config/balda
`); err != nil {
		t.Fatal(err)
	}
	setBaldaAdminPasswordGenerator(t, "generated-admin-password-for-bootstrap")
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	output := &bytes.Buffer{}
	command.SetIn(strings.NewReader(""))
	command.SetOut(output)
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"backoffice", "bootstrap-admin"})
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "administrator password: generated-admin-password-for-bootstrap") {
		t.Fatalf("generated password missing from bootstrap output: %q", output.String())
	}
	provider, err := state.Open(t.Context(), state.DatabaseConfig{
		Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(workingDir, ".config", "balda", "state.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	page, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: 1})
	if err != nil || len(page.Users) != 1 || page.Users[0].Username != usercmd.PrimaryUsername {
		t.Fatalf("administrator lookup: %+v, %v", page, err)
	}
	secret, found, err := provider.Users().GetCredentialSecret(t.Context(), page.Users[0].ID)
	if err != nil || !found || !userpassword.Verify(secret.PasswordHash, []byte("generated-admin-password-for-bootstrap")) {
		t.Fatalf("generated password verification: found=%t error=%v", found, err)
	}
	failedOutput := &bytes.Buffer{}
	failed, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	failed.SetIn(strings.NewReader(""))
	failed.SetOut(failedOutput)
	failed.SetErr(&bytes.Buffer{})
	failed.SetArgs([]string{"backoffice", "bootstrap-admin"})
	if err := failed.Execute(); err == nil {
		t.Fatal("second bootstrap unexpectedly replaced an active credential")
	}
	if strings.Contains(failedOutput.String(), "generated-admin-password-for-bootstrap") {
		t.Fatal("failed bootstrap printed a generated password")
	}
}

func TestStartRequiresAdministratorBeforeChannelConstruction(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  telegram:
    token: invalid-test-token
  state_dir: .config/balda
`); err != nil {
		t.Fatal(err)
	}
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"start"})
	err = command.Execute()
	if err == nil || !strings.Contains(err.Error(), "bootstrap-admin") {
		t.Fatalf("Start() error = %v, want administrator bootstrap instruction", err)
	}
	if _, statErr := os.Stat(filepath.Join(workingDir, ".config", "balda", "state.db")); statErr != nil {
		t.Fatalf("embedded migrations did not create selected database: %v", statErr)
	}
}

func TestStartMigrationFailureDoesNotBindBackoffice(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reserved.Addr().String()
	if err := reserved.Close(); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  telegram:
    token: invalid-test-token
  state_dir: .config/balda
  backoffice:
    listen_addr: %q
    public_url: "http://127.0.0.1:8095"
`, address)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), config); err != nil {
		t.Fatal(err)
	}
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "state.db"), "not a sqlite database"); err != nil {
		t.Fatal(err)
	}
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"start"})
	if err := command.Execute(); err == nil {
		t.Fatal("start succeeded with invalid database")
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("Backoffice address remained bound after migration failure: %v", err)
	}
	_ = listener.Close()
}

func TestStartUpgradesUsersBeforeAdministratorReadiness(t *testing.T) {
	fixture := readUserUpgradeFixture(t)
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	if err := writeFile(filepath.Join(workingDir, ".config", "balda", "config.yaml"), `runtime:
  providers:
    balda_agent:
      type: opencode_acp
      opencode_acp:
        model: opencode/big-pickle
balda:
  provider: balda_agent
  telegram:
    token: invalid-test-token
  state_dir: .config/balda
`); err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(workingDir, ".config", "balda", "state.db")
	seedUserUpgradeDatabase(t, databasePath, fixture)
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"start"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "bootstrap-admin") {
		t.Fatalf("Start() error = %v, want administrator bootstrap instruction", err)
	}
	provider, err := state.Open(t.Context(), state.DatabaseConfig{
		Type: "sqlite", SQLite: state.SQLiteConfig{Path: databasePath},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	owner, found, err := provider.Users().GetUserByBinding(t.Context(), "telegram", "101")
	if err != nil || !found || !owner.Primary || owner.Credential.State != usercmd.CredentialStateDisabled {
		t.Fatalf("owner after startup readiness check = %+v, found=%t, err=%v", owner, found, err)
	}
}

func readUserUpgradeFixture(t *testing.T) []byte {
	t.Helper()
	fixture, err := os.ReadFile("../../internal/apps/balda/state/testdata/sqlite_v36.sql")
	if err != nil {
		t.Fatal(err)
	}
	return fixture
}

func seedUserUpgradeDatabase(t *testing.T, path string, fixture []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.ExecContext(t.Context(), string(fixture)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO balda_app_kv (namespace, key, value_json, updated_at)
  VALUES ('balda.app', 'owner', '{"user_id":101,"chat_id":909,"registered_at":"2026-09-23T11:00:00Z"}', '2026-09-23T11:00:00Z');
  INSERT INTO balda_collaborators (user_id, username, first_name, added_by, added_at)
  VALUES ('202', 'operator', 'Op', '101', '2026-09-23T11:00:00Z')`); err != nil {
		t.Fatal(err)
	}
}
