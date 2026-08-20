package sessionturn

import (
	"context"
	"errors"
	"strings"
	"testing"

	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	adkrunner "google.golang.org/adk/v2/runner"
)

const (
	testMemoryUpdatedAt = "2026-08-20T10:00:00Z"
	testPriorMemoryAt   = "2026-08-20T09:00:00Z"
)

func TestRunnerRequiresSessionManager(t *testing.T) {
	t.Parallel()

	runner := New(nil, nil, nil, zerolog.Nop())
	err := runner.RunSessionTurnPayload(context.Background(), turncmd.SessionTurnPayload{})
	if err == nil || !strings.Contains(err.Error(), "session manager is unavailable") {
		t.Fatalf("RunSessionTurnPayload() error = %v, want missing session manager", err)
	}
}

func TestRunnerRefreshesCompleteMemoryWhenCursorChanged(t *testing.T) {
	t.Parallel()

	executor := &testExecutor{}
	runner := newMemoryTestRunner(&testActiveSession{stateErr: errors.New("metadata must take precedence")}, executor, testMemoryProvider{
		snapshot: MemorySnapshot{
			Content:   "first fact\n\nsecond fact",
			Version:   2,
			UpdatedAt: testMemoryUpdatedAt,
		},
	})
	payload := testTurnPayload()
	payload.Metadata = &turncmd.SessionTurnMetadata{LatestMemoryAt: testPriorMemoryAt}

	if err := runner.RunSessionTurnPayload(context.Background(), payload); err != nil {
		t.Fatalf("RunSessionTurnPayload() error = %v", err)
	}
	request := executor.singleRequest(t)
	if request.MemoryRefresh.Content != "first fact\n\nsecond fact" ||
		request.MemoryRefresh.UpdatedAt != testMemoryUpdatedAt {
		t.Fatalf("MemoryRefresh = %+v, want complete current snapshot", request.MemoryRefresh)
	}
	if len(request.MemoryRunOptions) != 1 {
		t.Fatalf("MemoryRunOptions count = %d, want 1", len(request.MemoryRunOptions))
	}
	if request.Payload.Metadata == nil || request.Payload.Metadata.LatestMemoryAt != testMemoryUpdatedAt {
		t.Fatalf("payload metadata = %+v, want current timestamp", request.Payload.Metadata)
	}
}

func TestRunnerDoesNotRefreshMemoryWhenCursorMatchesInstant(t *testing.T) {
	t.Parallel()

	executor := &testExecutor{}
	runner := newMemoryTestRunner(&testActiveSession{}, executor, testMemoryProvider{
		snapshot: MemorySnapshot{Content: "fact", Version: 1, UpdatedAt: testMemoryUpdatedAt},
	})
	payload := testTurnPayload()
	payload.Metadata = &turncmd.SessionTurnMetadata{LatestMemoryAt: "2026-08-20T16:00:00+06:00"}

	if err := runner.RunSessionTurnPayload(context.Background(), payload); err != nil {
		t.Fatalf("RunSessionTurnPayload() error = %v", err)
	}
	request := executor.singleRequest(t)
	if request.MemoryRefresh != (MemoryRefresh{}) || len(request.MemoryRunOptions) != 0 {
		t.Fatalf("unchanged memory request = %+v, options = %d", request.MemoryRefresh, len(request.MemoryRunOptions))
	}
	if request.Payload.Metadata == nil || request.Payload.Metadata.LatestMemoryAt != testMemoryUpdatedAt {
		t.Fatalf("payload metadata = %+v, want canonical current timestamp", request.Payload.Metadata)
	}
}

func TestRunnerUsesRuntimeMemoryCursorWhenTurnMetadataMissing(t *testing.T) {
	t.Parallel()

	executor := &testExecutor{}
	session := &testActiveSession{state: map[string]any{baldaMemoryUpdatedAtKey: testMemoryUpdatedAt}}
	runner := newMemoryTestRunner(session, executor, testMemoryProvider{
		snapshot: MemorySnapshot{Content: "fact", Version: 1, UpdatedAt: testMemoryUpdatedAt},
	})

	if err := runner.RunSessionTurnPayload(context.Background(), testTurnPayload()); err != nil {
		t.Fatalf("RunSessionTurnPayload() error = %v", err)
	}
	request := executor.singleRequest(t)
	if request.MemoryRefresh != (MemoryRefresh{}) || len(request.MemoryRunOptions) != 0 {
		t.Fatalf("runtime-equal memory request = %+v, options = %d", request.MemoryRefresh, len(request.MemoryRunOptions))
	}
	if request.Payload.Metadata == nil || request.Payload.Metadata.LatestMemoryAt != testMemoryUpdatedAt {
		t.Fatalf("payload metadata = %+v, want current timestamp", request.Payload.Metadata)
	}
}

func TestRunnerMissingCursorPerformsFullRefresh(t *testing.T) {
	t.Parallel()

	executor := &testExecutor{}
	runner := newMemoryTestRunner(&testActiveSession{}, executor, testMemoryProvider{
		snapshot: MemorySnapshot{Content: "all facts", Version: 3, UpdatedAt: testMemoryUpdatedAt},
	})

	if err := runner.RunSessionTurnPayload(context.Background(), testTurnPayload()); err != nil {
		t.Fatalf("RunSessionTurnPayload() error = %v", err)
	}
	request := executor.singleRequest(t)
	if request.MemoryRefresh.Content != "all facts" || len(request.MemoryRunOptions) != 1 {
		t.Fatalf("missing-cursor memory request = %+v, options = %d", request.MemoryRefresh, len(request.MemoryRunOptions))
	}
}

func TestRunnerDisabledOrEmptyMemoryLeavesTurnUnchanged(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  *testActiveSession
		provider MemoryStateProvider
	}{
		{
			name:     "disabled",
			session:  &testActiveSession{stateErr: errors.New("must not read state")},
			provider: disabledMemoryProvider{},
		},
		{
			name:     "empty",
			session:  &testActiveSession{stateErr: errors.New("must not read state")},
			provider: testMemoryProvider{snapshot: MemorySnapshot{}},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := &testExecutor{}
			runner := newMemoryTestRunner(test.session, executor, test.provider)
			if err := runner.RunSessionTurnPayload(context.Background(), testTurnPayload()); err != nil {
				t.Fatalf("RunSessionTurnPayload() error = %v", err)
			}
			request := executor.singleRequest(t)
			if request.MemoryRefresh != (MemoryRefresh{}) || len(request.MemoryRunOptions) != 0 {
				t.Fatalf("memory request = %+v, options = %d, want unchanged", request.MemoryRefresh, len(request.MemoryRunOptions))
			}
		})
	}
}

func TestRunnerRefreshesMemoryAfterSessionRestore(t *testing.T) {
	t.Parallel()

	executor := &testExecutor{}
	accessor := &testSessionAccessor{
		getErr:   errors.New("not active"),
		restored: &testActiveSession{state: map[string]any{baldaMemoryUpdatedAtKey: testPriorMemoryAt}},
	}
	runner := New(accessor, executor, testMemoryProvider{
		snapshot: MemorySnapshot{Content: "restored facts", Version: 2, UpdatedAt: testMemoryUpdatedAt},
	}, zerolog.Nop())

	if err := runner.RunSessionTurnPayload(context.Background(), testTurnPayload()); err != nil {
		t.Fatalf("RunSessionTurnPayload() error = %v", err)
	}
	if got := executor.singleRequest(t).MemoryRefresh.Content; got != "restored facts" {
		t.Fatalf("MemoryRefresh.Content = %q, want restored facts", got)
	}
}

func TestRunnerMemoryErrorsPreventProviderExecution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		payload  turncmd.SessionTurnPayload
		session  *testActiveSession
		provider testMemoryProvider
		wantErr  string
	}{
		{
			name:     "snapshot read",
			payload:  testTurnPayload(),
			session:  &testActiveSession{},
			provider: testMemoryProvider{err: errors.New("read failed")},
			wantErr:  "snapshot balda memory",
		},
		{
			name: "turn cursor",
			payload: func() turncmd.SessionTurnPayload {
				payload := testTurnPayload()
				payload.Metadata = &turncmd.SessionTurnMetadata{LatestMemoryAt: "invalid"}
				return payload
			}(),
			session:  &testActiveSession{},
			provider: testMemoryProvider{snapshot: MemorySnapshot{Content: "fact", UpdatedAt: testMemoryUpdatedAt}},
			wantErr:  "turn memory cursor",
		},
		{
			name:     "runtime cursor",
			payload:  testTurnPayload(),
			session:  &testActiveSession{state: map[string]any{baldaMemoryUpdatedAtKey: "invalid"}},
			provider: testMemoryProvider{snapshot: MemorySnapshot{Content: "fact", UpdatedAt: testMemoryUpdatedAt}},
			wantErr:  "session memory cursor",
		},
		{
			name:     "runtime state read",
			payload:  testTurnPayload(),
			session:  &testActiveSession{stateErr: errors.New("state unavailable")},
			provider: testMemoryProvider{snapshot: MemorySnapshot{Content: "fact", UpdatedAt: testMemoryUpdatedAt}},
			wantErr:  "read balda memory updated_at",
		},
		{
			name:     "snapshot timestamp",
			payload:  testTurnPayload(),
			session:  &testActiveSession{},
			provider: testMemoryProvider{snapshot: MemorySnapshot{Content: "fact", UpdatedAt: "invalid"}},
			wantErr:  "snapshot updated_at",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			executor := &testExecutor{}
			runner := newMemoryTestRunner(test.session, executor, test.provider)
			err := runner.RunSessionTurnPayload(context.Background(), test.payload)
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("RunSessionTurnPayload() error = %v, want containing %q", err, test.wantErr)
			}
			if len(executor.requests) != 0 {
				t.Fatalf("executor requests = %d, want 0", len(executor.requests))
			}
		})
	}
}

func newMemoryTestRunner(session ActiveSession, executor Executor, provider MemoryStateProvider) *Runner {
	return New(&testSessionAccessor{active: session}, executor, provider, zerolog.Nop())
}

func testTurnPayload() turncmd.SessionTurnPayload {
	return turncmd.SessionTurnPayload{
		Locator: baldasession.SessionLocator{
			SessionID:   "tg-1-0",
			ChannelType: "telegram",
		},
		UserID: "user-1",
	}
}

type testExecutor struct {
	requests []Request
}

func (e *testExecutor) ExecuteSessionTurn(_ context.Context, request Request) error {
	e.requests = append(e.requests, request)
	return nil
}

func (e *testExecutor) singleRequest(t *testing.T) Request {
	t.Helper()
	if len(e.requests) != 1 {
		t.Fatalf("executor requests = %d, want 1", len(e.requests))
	}
	return e.requests[0]
}

type testMemoryProvider struct {
	snapshot MemorySnapshot
	err      error
}

type disabledMemoryProvider struct{}

func (disabledMemoryProvider) Enabled() bool { return false }

func (disabledMemoryProvider) Snapshot(context.Context) (MemorySnapshot, error) {
	return MemorySnapshot{}, errors.New("unexpected Snapshot call")
}

func (testMemoryProvider) Enabled() bool { return true }

func (p testMemoryProvider) Snapshot(context.Context) (MemorySnapshot, error) {
	return p.snapshot, p.err
}

type testSessionAccessor struct {
	active     ActiveSession
	getErr     error
	restored   ActiveSession
	restoreErr error
}

func (a *testSessionAccessor) GetSession(SessionLocator) (ActiveSession, error) {
	return a.active, a.getErr
}

func (a *testSessionAccessor) RestoreSession(context.Context, SessionContext) (ActiveSession, error) {
	return a.restored, a.restoreErr
}

func (a *testSessionAccessor) EnsureSession(context.Context, SessionContext, string) (ActiveSession, error) {
	return nil, errors.New("unexpected EnsureSession call")
}

type testActiveSession struct {
	state    map[string]any
	stateErr error
}

func (*testActiveSession) GetRunner() *adkrunner.Runner { return nil }
func (*testActiveSession) GetSessionID() string         { return "tg-1-0" }
func (*testActiveSession) GetAgentSessionID() string    { return "agent-session-1" }
func (*testActiveSession) GetUserID() string            { return "user-1" }

func (s *testActiveSession) RuntimeStateValue(_ context.Context, key string) (any, bool, error) {
	if s.stateErr != nil {
		return nil, false, s.stateErr
	}
	value, ok := s.state[key]
	return value, ok, nil
}
