package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
)

func TestBackofficeMaintenanceUsesSelectedBaldaState(t *testing.T) {
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
	provider, err := state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	registeredAt := time.Date(2026, 9, 23, 11, 0, 0, 0, time.UTC)
	if err := provider.AppKV().SetJSON(t.Context(), "owner", map[string]any{
		"user_id": int64(101), "chat_id": int64(909), "registered_at": registeredAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Collaborators().AddCollaborator(t.Context(), authcmd.Collaborator{
		UserID: "202", Username: "operator", AddedBy: "101", AddedAt: registeredAt,
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(workingDir, "credentials.json")
	migrateOutput := &bytes.Buffer{}
	migrate, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	migrate.SetOut(migrateOutput)
	migrate.SetErr(&bytes.Buffer{})
	migrate.SetArgs([]string{"backoffice", "migrate-users", "--credentials-output", manifestPath})
	if err := migrate.Execute(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credentials mode = %v, want 0600", info.Mode().Perm())
	}
	manifest, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var credentials struct {
		Users []struct {
			TemporaryPassword string `json:"temporary_password"`
		} `json:"users"`
	}
	if err := json.Unmarshal(manifest, &credentials); err != nil {
		t.Fatal(err)
	}
	if len(credentials.Users) != 2 {
		t.Fatalf("migrated users = %d, want 2", len(credentials.Users))
	}
	for _, user := range credentials.Users {
		if strings.Contains(migrateOutput.String(), user.TemporaryPassword) {
			t.Fatal("migration leaked temporary password")
		}
	}
	provider, err = state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
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
	bootstrap, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	bootstrap.SetIn(strings.NewReader("correct horse battery staple\n"))
	bootstrap.SetOut(bootstrapOutput)
	bootstrap.SetErr(&bytes.Buffer{})
	bootstrap.SetArgs([]string{"backoffice", "bootstrap-admin", "--reset"})
	if err := bootstrap.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(bootstrapOutput.String(), "correct horse battery staple") {
		t.Fatal("bootstrap leaked password")
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
	collaborator, found, err := provider.Users().GetUserByBinding(t.Context(), "telegram", "202")
	if err != nil || !found || users.BotCapability(collaborator) != usercmd.BotCapabilityCollaborator {
		t.Fatalf("collaborator binding = %+v, found=%t, error=%v", collaborator, found, err)
	}
	for _, user := range page.Users {
		if user.Primary && user.Credential.State != usercmd.CredentialStateActive {
			t.Fatalf("primary credential state = %q, want active", user.Credential.State)
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
	command.SetArgs([]string{"backoffice", "bootstrap-admin", "--username", "admin"})
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
	if err != nil || len(page.Users) != 1 || !page.Users[0].Primary {
		t.Fatalf("fresh administrator = %+v, error = %v", page, err)
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

func TestStartRequiresLegacyUserConversion(t *testing.T) {
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
	provider, err := state.Open(t.Context(), state.DatabaseConfig{
		Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(workingDir, ".config", "balda", "state.db")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := provider.AppKV().SetJSON(t.Context(), "owner", map[string]any{
		"user_id": int64(101), "chat_id": int64(909), "registered_at": time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	command, err := newRootCommand()
	if err != nil {
		t.Fatal(err)
	}
	command.SetOut(&bytes.Buffer{})
	command.SetErr(&bytes.Buffer{})
	command.SetArgs([]string{"start"})
	if err := command.Execute(); err == nil || !strings.Contains(err.Error(), "migrate-users") {
		t.Fatalf("Start() error = %v, want legacy conversion instruction", err)
	}
}
