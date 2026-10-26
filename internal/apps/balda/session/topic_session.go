package session

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
)

// TopicSession represents a single channel session's provider-backed agent session.
type TopicSession struct {
	sessionID         string
	userID            string
	locator           SessionLocator
	kind              Kind
	ownerJobID        string
	agentName         string
	runtimeMu         sync.RWMutex
	runtime           *RuntimeSlot
	recoveryRequired  bool
	workspaceDir      string
	branchName        string
	startupNotice     string
	pendingBoundaries map[BoundaryReason]boundaryAttempt
	runtimeDeleted    bool
	runtimeClosed     bool
	hooksStarted      bool
	hooksEnded        bool
}

type boundaryAttempt struct {
	boundary  SessionBoundary
	published bool
}

func (s *TopicSession) GetRunner() *runner.Runner {
	if s == nil {
		return nil
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	if s.recoveryRequired || s.runtime == nil {
		return nil
	}
	return s.runtime.value.runtime.Runner
}

// AcquireRuntime leases the current coherent runtime view and refuses leases
// while publication recovery is required or drain has begun.
func (s *TopicSession) AcquireRuntime() (*RuntimeLease, error) {
	if s == nil {
		return nil, ErrRuntimeUnavailable
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	if s.recoveryRequired {
		return nil, ErrRuntimeRecoveryRequired
	}
	return s.runtime.acquire()
}

// DrainRuntime prevents new leases and waits for existing leases to release.
// A canceled deadline never forces resources closed while they remain in use.
func (s *TopicSession) DrainRuntime(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.runtimeMu.Lock()
	runtime := s.runtime
	if runtime != nil {
		runtime.beginDrain()
	}
	s.runtimeMu.Unlock()
	if runtime == nil {
		return nil
	}
	return runtime.waitDrain(ctx)
}

func (s *TopicSession) GetSessionID() string {
	return s.sessionID
}

func (s *TopicSession) GetAgentSessionID() string {
	if value, ok := s.runtimeValue(); ok {
		return strings.TrimSpace(value.sessionID)
	}
	return s.sessionID
}

func (s *TopicSession) GetUserID() string {
	return s.userID
}

// GetKind returns the immutable execution kind selected at admission.
func (s *TopicSession) GetKind() Kind { return s.kind }

// GetOwnerJobID returns the service job that owns this session, when present.
func (s *TopicSession) GetOwnerJobID() string { return s.ownerJobID }

func (s *TopicSession) GetWorkspaceDir() string {
	return s.workspaceDir
}

func (s *TopicSession) GetBranchName() string {
	return s.branchName
}

// GetRuntimeSnapshotID returns the immutable capability snapshot bound to the session runtime.
func (s *TopicSession) GetRuntimeSnapshotID() string {
	runtime := s.currentRuntime()
	if runtime == nil {
		return ""
	}
	return runtime.value.runtime.RuntimeSnapshotID
}

func (s *TopicSession) GetAgentName() string {
	return s.agentName
}

func (s *TopicSession) currentRuntime() *RuntimeSlot {
	if s == nil {
		return nil
	}
	s.runtimeMu.RLock()
	defer s.runtimeMu.RUnlock()
	return s.runtime
}

func (s *TopicSession) runtimeValue() (runtimeSlotValue, bool) {
	runtime := s.currentRuntime()
	if runtime == nil {
		return runtimeSlotValue{}, false
	}
	return runtime.value, true
}

// RuntimeStateValue returns a value from the current persisted runtime session.
func (s *TopicSession) RuntimeStateValue(ctx context.Context, key string) (any, bool, error) {
	lease, err := s.AcquireRuntime()
	if errors.Is(err, ErrRuntimeUnavailable) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer lease.Release()
	if lease.Session() == nil || lease.SessionService() == nil {
		return nil, false, nil
	}
	current, err := lease.SessionService().Get(ctx, &session.GetRequest{
		AppName:   lease.AppName(),
		UserID:    lease.UserID(),
		SessionID: lease.SessionID(),
	})
	if err != nil {
		return nil, false, fmt.Errorf("get runtime session: %w", err)
	}
	if current == nil || current.Session == nil || current.Session.State() == nil {
		return nil, false, nil
	}
	stateValue, err := current.Session.State().Get(strings.TrimSpace(key))
	if err != nil {
		return nil, false, nil
	}
	return stateValue, true, nil
}

func (s *TopicSession) UpdateRuntimeState(ctx context.Context, state map[string]any) error {
	if len(state) == 0 {
		return nil
	}
	lease, err := s.AcquireRuntime()
	if errors.Is(err, ErrRuntimeUnavailable) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lease.Release()
	if lease.Session() == nil || lease.SessionService() == nil {
		return nil
	}
	updater, ok := lease.SessionService().(interface {
		UpdateSessionState(ctx context.Context, appName string, userID string, sessionID string, state map[string]any) (session.Session, error)
	})
	if !ok {
		return nil
	}
	_, err = updater.UpdateSessionState(ctx, lease.AppName(), lease.UserID(), lease.SessionID(), state)
	return err
}

// GetArchiveID returns the immutable identity of this conversational lifetime.
func (s *TopicSession) GetArchiveID() string {
	if runtime := s.currentRuntime(); runtime != nil {
		return runtime.value.archiveID
	}
	return ""
}
