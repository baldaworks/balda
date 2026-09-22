package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestRootCommandSurfaceHasNoPasswordFlag(t *testing.T) {
	t.Parallel()
	command := newRootCommand()
	names := make(map[string]bool)
	for _, child := range command.Commands() {
		names[child.Name()] = true
		if child.Name() == "bootstrap-admin" && child.Flags().Lookup("password") != nil {
			t.Fatal("bootstrap-admin exposes a password flag")
		}
	}
	for _, name := range []string{"serve", "validate", "migrate-users", "bootstrap-admin"} {
		if !names[name] {
			t.Fatalf("missing command %q", name)
		}
	}
}

func TestReadPassword(t *testing.T) {
	t.Parallel()
	want := "correct horse battery staple"
	got, err := readPassword(strings.NewReader(want + "\n"))
	if err != nil || string(got) != want {
		t.Fatalf("readPassword() = %q, %v", got, err)
	}
	if _, err := readPassword(strings.NewReader("short\n")); err == nil {
		t.Fatal("readPassword(short) error = nil")
	}
}

func TestMigrationAndBootstrapCommandsUseCanonicalState(t *testing.T) {
	workingDir := t.TempDir()
	t.Chdir(workingDir)
	configDir := filepath.Join(workingDir, ".config", "balda")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`runtime:
  providers: {}
  mcp_servers: {}
balda:
  working_dir: %q
  state_dir: ".config/balda"
  database:
    type: sqlite
    sqlite:
      path: "{{.StateDir}}/state.db"
  backoffice:
    listen_addr: "127.0.0.1:8095"
    public_url: "http://127.0.0.1:8095"
    access_token_ttl: "15m"
    refresh_token_ttl: "12h"
`, workingDir)
	if err := os.WriteFile(filepath.Join(configDir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	database := state.DatabaseConfig{Type: "sqlite", SQLite: state.SQLiteConfig{Path: filepath.Join(configDir, "state.db")}}
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
	migrate := newRootCommand()
	migrate.SetOut(migrateOutput)
	migrate.SetErr(&bytes.Buffer{})
	migrate.SetArgs([]string{"migrate-users", "--credentials-output", manifestPath})
	if err := migrate.Execute(); err != nil {
		t.Fatalf("migrate-users error = %v", err)
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
		t.Fatalf("manifest user count = %d, want 2", len(credentials.Users))
	}
	if strings.Contains(migrateOutput.String(), "temporary_password") || bytes.Contains(manifest, []byte("correct horse battery staple")) {
		t.Fatal("command output leaked credential material")
	}
	for _, credential := range credentials.Users {
		if strings.Contains(migrateOutput.String(), credential.TemporaryPassword) {
			t.Fatal("migration stdout contains a generated temporary password")
		}
	}
	provider, err = state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	usersPage, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil || len(usersPage.Users) != 2 {
		t.Fatalf("ListUsers(after migration) = %+v, %v", usersPage, err)
	}
	var migratedPrimary usercmd.User
	for _, user := range usersPage.Users {
		if user.Primary {
			migratedPrimary = user
		}
	}
	sessions, err := provider.Users().ListSessions(t.Context(), migratedPrimary.ID, usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil || len(sessions.Sessions) != 0 {
		t.Fatalf("sessions created by migration = %+v, %v", sessions, err)
	}
	familyCreatedAt := registeredAt.Add(time.Hour)
	refreshExpiry := familyCreatedAt.Add(12 * time.Hour)
	family := usercmd.SessionFamily{
		ID: "pre-bootstrap-family", UserID: migratedPrimary.ID, Assurance: usercmd.SessionAssuranceRestricted,
		CredentialVersion: migratedPrimary.Credential.Version,
		Access: usercmd.AccessCredential{
			Selector: "pre-bootstrap-access", VerifierDigest: []byte("access-digest"), ExpiresAt: familyCreatedAt.Add(15 * time.Minute),
		},
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
		t.Fatalf("CreateSession() error = %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	bootstrapOutput := &bytes.Buffer{}
	bootstrap := newRootCommand()
	bootstrap.SetIn(strings.NewReader("correct horse battery staple\n"))
	bootstrap.SetOut(bootstrapOutput)
	bootstrap.SetErr(&bytes.Buffer{})
	bootstrap.SetArgs([]string{"bootstrap-admin", "--reset"})
	if err := bootstrap.Execute(); err != nil {
		t.Fatalf("bootstrap-admin error = %v", err)
	}
	if strings.Contains(bootstrapOutput.String(), "correct horse battery staple") {
		t.Fatal("bootstrap output leaked password")
	}

	provider, err = state.Open(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	usersPage, err = provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: usercmd.MaxPageSize})
	if err != nil || len(usersPage.Users) != 2 {
		t.Fatalf("ListUsers() = %+v, %v", usersPage, err)
	}
	var primary usercmd.User
	for _, user := range usersPage.Users {
		if user.Primary {
			primary = user
		}
	}
	if primary.ID == "" || primary.Credential.State != usercmd.CredentialStateActive || primary.Credential.Version != 2 {
		t.Fatalf("primary after bootstrap = %+v", primary)
	}
	revoked, found, err := provider.Users().GetSessionByAccessSelector(t.Context(), family.Access.Selector)
	if err != nil || !found || revoked.Family.RevokedAt.IsZero() {
		t.Fatalf("session after bootstrap reset = %+v, found=%t, error=%v", revoked, found, err)
	}
	if len(revoked.Family.RefreshTokens) != 1 || revoked.Family.RefreshTokens[0].State != usercmd.RefreshTokenStateRevoked {
		t.Fatalf("refresh lineage after bootstrap reset = %+v", revoked.Family.RefreshTokens)
	}
}
