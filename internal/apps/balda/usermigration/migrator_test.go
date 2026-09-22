package usermigration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/authcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const testChannelTelegram = "telegram"

func TestMigratorBuildsCanonicalUsersAndSecureManifest(t *testing.T) {
	t.Parallel()
	store := &fakeMigrationStore{}
	now := time.Date(2026, 9, 23, 5, 0, 0, 0, time.UTC)
	random := make([]byte, 24*3)
	for i := range random {
		random[i] = byte(i + 1)
	}
	migrator, err := New(
		store,
		WithRandom(bytes.NewReader(random)),
		WithClock(func() time.Time { return now }),
		WithHasher(testPasswordHash),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	result, err := migrator.Migrate(t.Context(), Input{
		Owner: &LegacyOwner{
			UserID: 101, ChatID: 909, Subject: "slackagent:T1:U1", Bindings: []string{"telegram:101"}, RegisteredAt: now,
		},
		Collaborators:     []authcmd.Collaborator{{UserID: "202", Username: "operator", FirstName: "Op"}},
		CredentialsOutput: path,
	})
	if err != nil {
		t.Fatalf("Migrate() error = %v", err)
	}
	if !result.Applied || result.UserCount != 3 || result.BindingCount != 3 {
		t.Fatalf("Migrate() result = %+v", result)
	}
	if store.applyCalls != 1 || len(store.batch.Users) != 3 {
		t.Fatalf("applied batch calls/users = %d/%d", store.applyCalls, len(store.batch.Users))
	}
	var primary usercmd.MigrationUser
	for _, entry := range store.batch.Users {
		if entry.User.Primary {
			primary = entry
		}
		if entry.User.Credential.State != usercmd.CredentialStateTemporary || !entry.User.Credential.MustChange {
			t.Fatalf("migrated credential = %+v", entry.User.Credential)
		}
		if entry.Binding == nil || entry.Binding.UserID != entry.User.ID || !strings.HasPrefix(entry.Secret.PasswordHash, "hash:") {
			t.Fatalf("migrated entry = %+v", entry)
		}
	}
	if primary.Binding == nil || primary.Binding.ChannelType != testChannelTelegram || primary.Binding.Principal != "101" {
		t.Fatalf("primary entry = %+v", primary)
	}
	if !strings.Contains(primary.Binding.Provenance, "chat_id=909") || !strings.Contains(primary.Binding.Provenance, "registered_at=") {
		t.Fatalf("primary provenance = %q", primary.Binding.Provenance)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("manifest mode = %04o, want 0600", info.Mode().Perm())
	}
	manifest, err := readManifest(path)
	if err != nil {
		t.Fatal(err)
	}
	passwords := make(map[string]struct{}, len(manifest.Users))
	hashes := make(map[string]struct{}, len(store.batch.Users))
	for _, entry := range store.batch.Users {
		hashes[entry.Secret.PasswordHash] = struct{}{}
	}
	for _, credential := range manifest.Users {
		if _, exists := passwords[credential.TemporaryPassword]; exists {
			t.Fatalf("duplicate temporary password %q", credential.TemporaryPassword)
		}
		passwords[credential.TemporaryPassword] = struct{}{}
		if _, persistedPlaintext := hashes[credential.TemporaryPassword]; persistedPlaintext {
			t.Fatal("plaintext temporary password reached persistence batch")
		}
	}
	if len(passwords) != 3 {
		t.Fatalf("manifest password count = %d, want 3", len(passwords))
	}

	result, err = migrator.Migrate(t.Context(), Input{
		Owner:             &LegacyOwner{UserID: 101, ChatID: 909, Subject: "slackagent:T1:U1", Bindings: []string{"telegram:101"}, RegisteredAt: now},
		Collaborators:     []authcmd.Collaborator{{UserID: "202", Username: "operator", FirstName: "Op"}},
		CredentialsOutput: path,
	})
	if err != nil || result.Applied || store.applyCalls != 1 {
		t.Fatalf("Migrate(repeated) = %+v, calls=%d, error=%v", result, store.applyCalls, err)
	}
}

func TestMigratorReusesManifestAfterCommitFailure(t *testing.T) {
	t.Parallel()
	store := &fakeMigrationStore{applyErr: errors.New("injected commit failure")}
	now := time.Date(2026, 9, 23, 6, 0, 0, 0, time.UTC)
	migrator, err := New(
		store,
		WithRandom(bytes.NewReader(bytes.Repeat([]byte{0x5a}, 24))),
		WithClock(func() time.Time { return now }),
		WithHasher(testPasswordHash),
	)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	input := Input{Owner: &LegacyOwner{UserID: 101}, CredentialsOutput: path}
	if _, err := migrator.Migrate(t.Context(), input); err == nil {
		t.Fatal("Migrate(first) error = nil")
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	store.applyErr = nil
	result, err := migrator.Migrate(t.Context(), input)
	if err != nil || !result.Applied {
		t.Fatalf("Migrate(retry) = %+v, %v", result, err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("retry changed credentials manifest")
	}
}

func TestMigratorDoesNotOverwriteExistingManifest(t *testing.T) {
	t.Parallel()
	store := &fakeMigrationStore{}
	migrator, err := New(store, WithHasher(testPasswordHash))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "credentials.json")
	want := []byte("operator-owned\n")
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatal(err)
	}
	_, err = migrator.Migrate(t.Context(), Input{Owner: &LegacyOwner{UserID: 101}, CredentialsOutput: path})
	if err == nil {
		t.Fatal("Migrate() error = nil")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("existing manifest changed: got %q, want %q", got, want)
	}
}

func TestMigratorRejectsCollisionAndMalformedSubject(t *testing.T) {
	t.Parallel()
	store := &fakeMigrationStore{}
	migrator, err := New(store, WithHasher(func(password []byte) (string, error) { return string(password), nil }))
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		input Input
	}{
		{
			name: "owner collaborator collision",
			input: Input{
				Owner: &LegacyOwner{UserID: 101}, Collaborators: []authcmd.Collaborator{{UserID: "101"}},
				CredentialsOutput: filepath.Join(t.TempDir(), "collision.json"),
			},
		},
		{
			name: "malformed subject",
			input: Input{
				Owner:             &LegacyOwner{Subject: "future:user"},
				CredentialsOutput: filepath.Join(t.TempDir(), "malformed.json"),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := migrator.Migrate(t.Context(), tt.input); err == nil {
				t.Fatal("Migrate() error = nil")
			}
		})
	}
}

func TestPrepareUsesSelectedPrimaryInFingerprint(t *testing.T) {
	t.Parallel()
	input := Input{Owner: &LegacyOwner{Subject: "slackagent:T1:U1", Bindings: []string{testChannelTelegram + ":101"}}}
	telegramPrimary, err := prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	input.PrimarySubject = "slackagent:T1:U1"
	slackPrimary, err := prepare(input)
	if err != nil {
		t.Fatal(err)
	}
	if telegramPrimary.primaryUserID == slackPrimary.primaryUserID {
		t.Fatal("explicit primary did not change primary user")
	}
	if telegramPrimary.fingerprint == slackPrimary.fingerprint {
		t.Fatal("source fingerprint omitted selected primary")
	}
	for i := range telegramPrimary.users {
		if telegramPrimary.users[i].user.Username != slackPrimary.users[i].user.Username {
			t.Fatal("primary selection changed deterministic username")
		}
	}
}

func testPasswordHash(password []byte) (string, error) {
	sum := sha256.Sum256(password)
	return "hash:" + hex.EncodeToString(sum[:]), nil
}

type fakeMigrationStore struct {
	applied    bool
	applyErr   error
	applyCalls int
	batch      usercmd.UserMigration
}

func (s *fakeMigrationStore) UserMigrationApplied(context.Context, string) (bool, error) {
	return s.applied, nil
}

func (s *fakeMigrationStore) ApplyUserMigration(_ context.Context, batch usercmd.UserMigration) (bool, error) {
	s.applyCalls++
	s.batch = batch
	if s.applyErr != nil {
		return false, s.applyErr
	}
	s.applied = true
	return true, nil
}
