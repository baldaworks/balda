package backoffice

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/userpassword"
)

const validPassword = "correct horse battery staple"

func newHTTPAppTestState(t *testing.T) (state.Provider, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := state.NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider, path
}

func createAccessTestUser(t *testing.T, store usercmd.Store, user usercmd.User) usercmd.User {
	t.Helper()
	if user.Username == "" {
		user.Username = user.ID
	}
	if user.NormalizedUsername == "" {
		user.NormalizedUsername = user.Username
	}
	if user.DisplayName == "" {
		user.DisplayName = user.Username
	}
	if user.CreatedAt.IsZero() {
		user.CreatedAt = time.Now().UTC()
	}
	if user.UpdatedAt.IsZero() {
		user.UpdatedAt = user.CreatedAt
	}
	hash, err := userpassword.Hash([]byte(validPassword))
	if err != nil {
		t.Fatal(err)
	}
	err = store.CreateUser(t.Context(), user, usercmd.CredentialSecret{UserID: user.ID, PasswordHash: hash},
		usercmd.AuditEvent{ID: "create-" + user.ID, Action: usercmd.AuditActionCredentialChanged, Outcome: usercmd.AuditOutcomeSucceeded,
			TargetType: usercmd.AuditTargetUser, TargetID: user.ID, Source: "test", OccurredAt: user.CreatedAt})
	if err != nil {
		t.Fatal(err)
	}
	return user
}
