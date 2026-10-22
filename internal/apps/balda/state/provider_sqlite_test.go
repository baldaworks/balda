//go:build integration && sqlite

package state

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	adksession "google.golang.org/adk/v2/session"
)

func TestSQLiteProviderContract(t *testing.T) {
	// Contract cases need isolated databases, but repeating every migration for
	// each case adds no coverage. Seed each fresh path from one migrated template.
	templatePath := filepath.Join(t.TempDir(), "contract-template.db")
	provider, err := NewSQLiteProvider(t.Context(), templatePath)
	if err != nil {
		t.Fatalf("create contract template: %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("close contract template: %v", err)
	}
	template, err := os.ReadFile(templatePath)
	if err != nil {
		t.Fatalf("read contract template: %v", err)
	}

	runProviderContract(t, func(*testing.T) contractOpener {
		return func(ctx context.Context, path string) (Provider, error) {
			if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
				if err := os.WriteFile(path, template, 0o600); err != nil {
					return nil, fmt.Errorf("seed SQLite contract database: %w", err)
				}
			} else if err != nil {
				return nil, fmt.Errorf("inspect SQLite contract database: %w", err)
			}
			return NewSQLiteProvider(ctx, path)
		}
	})
}

func TestSQLiteRuntimeSessionDeleteRetriesAfterPoolContention(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })

	service := provider.RuntimeSessions()
	const appName = "norma-balda"
	const userID = "tg-101"
	const sessionID = "tg-10-42"
	if _, err := service.Create(t.Context(), &adksession.CreateRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	conn, err := provider.(*sqliteProvider).db.Conn(t.Context())
	if err != nil {
		t.Fatalf("Conn() error = %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	deleteCtx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	err = service.Delete(deleteCtx, &adksession.DeleteRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Delete() with occupied pool error = %v, want deadline exceeded", err)
	}
	if err := conn.Close(); err != nil {
		t.Fatalf("Close() held connection: %v", err)
	}
	if _, err := service.Get(t.Context(), &adksession.GetRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatalf("Get() after failed delete: %v", err)
	}
	if err := service.Delete(t.Context(), &adksession.DeleteRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err != nil {
		t.Fatalf("Delete() retry error = %v", err)
	}
	if _, err := service.Get(t.Context(), &adksession.GetRequest{
		AppName: appName, UserID: userID, SessionID: sessionID,
	}); err == nil {
		t.Fatal("runtime session still exists after retry")
	}
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
