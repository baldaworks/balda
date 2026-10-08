package session

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/baldaworks/balda/internal/git"
	adksession "google.golang.org/adk/v2/session"
)

// CloseRunSession removes a private run session after execution settles.
// Repeating the call also cleans a persisted runtime or branch left by an interrupted close.
func (m *Manager) CloseRunSession(ctx context.Context, sessionID, userID string) error {
	sessionID, userID = strings.TrimSpace(sessionID), strings.TrimSpace(userID)
	if !isPrivateScheduleSessionID(sessionID) && !isPrivateWebhookSessionID(sessionID) || userID == "" {
		return fmt.Errorf("private run session and user ids are required")
	}
	m.mu.RLock()
	active := m.sessions[sessionID]
	m.mu.RUnlock()
	runtimeDeleted := false
	if active != nil {
		removed, err := m.removeActiveSession(ctx, active.locator,
			sessionCleanupOptions{deleteRuntimeSession: true, cleanupWorkspace: true}, BoundaryReasonClose)
		if err != nil {
			return err
		}
		runtimeDeleted = removed
	}
	if !runtimeDeleted {
		if m.runtimeManager == nil {
			return fmt.Errorf("balda runtime manager is required")
		}
		runtime, err := m.runtimeManager.Runtime(ctx)
		if err != nil {
			return err
		}
		if runtime == nil || runtime.SessionSvc == nil {
			return fmt.Errorf("session service is required")
		}
		appName := strings.TrimSpace(runtime.AppName)
		if appName == "" {
			appName = baldaRuntimeAppName
		}
		if err := runtime.SessionSvc.Delete(ctx, &adksession.DeleteRequest{
			AppName: appName, UserID: userID, SessionID: sessionID,
		}); err != nil {
			return err
		}
	}
	if m.workspaces != nil {
		path := m.workspaces.CanonicalWorkspaceDir(sessionID)
		workspaceFound := false
		if _, err := os.Stat(path); err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("inspect private schedule workspace: %w", err)
			}
		} else if err := m.workspaces.CleanupWorkspace(ctx, path); err != nil {
			return fmt.Errorf("cleanup private schedule workspace: %w", err)
		} else {
			workspaceFound = true
		}
		if !m.workspaceEnabled && !workspaceFound && !git.Available(ctx, m.workingDir) {
			return nil
		}
		if err := m.workspaces.DeleteBranch(ctx, "norma/balda/"+sessionID); err != nil {
			return fmt.Errorf("cleanup private schedule branch: %w", err)
		}
	}
	return nil
}

func isPrivateScheduleSessionID(sessionID string) bool {
	return hasPrivateSessionID(sessionID, "sch-")
}

func isPrivateWebhookSessionID(sessionID string) bool {
	return hasPrivateSessionID(sessionID, "wh-")
}

func hasPrivateSessionID(sessionID, prefix string) bool {
	if len(sessionID) != len(prefix)+32 || !strings.HasPrefix(sessionID, prefix) {
		return false
	}
	for _, digit := range sessionID[len(prefix):] {
		if digit < '0' || digit > '9' {
			if digit < 'a' || digit > 'f' {
				return false
			}
		}
	}
	return true
}
