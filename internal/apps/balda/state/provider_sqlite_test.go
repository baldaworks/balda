//go:build integration && sqlite

package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	adksession "google.golang.org/adk/v2/session"
)

func TestSQLiteProviderContract(t *testing.T) {
	runProviderContract(t, func(*testing.T) contractOpener { return NewSQLiteProvider })
}

func TestSQLiteRuntimeSessionsListReleasesConnection(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	service := provider.RuntimeSessions()
	const appName = "balda"
	const userID = "user-1"
	const sessionID = "session-1"
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	listCtx, cancelList := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelList()
	listed, err := service.List(listCtx, &adksession.ListRequest{AppName: appName, UserID: userID})
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ID() != sessionID {
		t.Fatalf("List() sessions = %+v, want session %q", listed.Sessions, sessionID)
	}

	deleteCtx, cancelDelete := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancelDelete()
	if err := service.Delete(deleteCtx, &adksession.DeleteRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
}
