package natsbus

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
	"github.com/rs/zerolog"
)

func TestSessionMemoryEndToEndNativeProviderScopeAndBoundary(t *testing.T) {
	t.Parallel()

	invoker := &nativeE2EInvoker{}
	deriver, err := sessionmemoryapp.NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	provider, err := sessionmemoryapp.NewNativeProvider(sessionmemorytest.NewStore(), deriver, invoker)
	if err != nil {
		t.Fatalf("NewNativeProvider() error = %v", err)
	}
	bus := StartTestRuntime(t, enabledSessionMemoryExecutionConfig()).Bus
	resolver := sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
		baldatelegram.ChannelType: baldatelegram.ClassifyLocatorScope,
	})
	capture := sessionmemoryapp.NewTurnCapture(bus.SessionMemoryExportPublisher(), resolver)
	boundaryCapture := sessionmemoryapp.NewBoundaryCapture(bus.SessionMemoryExportPublisher(), resolver)
	locator := baldatelegram.NewLocator(123, 0)
	completedAt := time.Date(2026, 8, 3, 5, 6, 7, 0, time.UTC)
	turnResult, err := capture.Capture(context.Background(), sessionmemoryapp.CaptureRequest{
		UserText:       "remember the release checklist",
		AssistantText:  "the release checklist is ready",
		Locator:        locator,
		SessionID:      locator.SessionID,
		AgentSessionID: "adk-7",
		SourceTurnID:   "telegram:message:9",
		CompletedAt:    completedAt,
	})
	if err != nil {
		t.Fatalf("TurnCapture.Capture() error = %v", err)
	}
	boundaryResult, err := boundaryCapture.Capture(context.Background(), sessionmemoryapp.BoundaryCaptureRequest{
		Locator:        locator,
		SessionID:      locator.SessionID,
		AgentSessionID: "adk-7",
		TransitionID:   "rotation-1",
		Reason:         sessionmemory.BoundaryReasonRotation,
		OccurredAt:     completedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("BoundaryCapture.Capture() error = %v", err)
	}

	worker, err := sessionmemoryapp.NewWorker(bus.SessionMemoryTransport(), provider, sessionmemoryapp.Config{
		Enabled:          true,
		MaxAttempts:      2,
		RetryBaseDelay:   5 * time.Millisecond,
		RetryMaxDelay:    5 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	if err := worker.Start(context.Background()); err != nil {
		t.Fatalf("worker.Start() error = %v", err)
	}
	waitForSessionMemoryWorker(t, 3*time.Second, func() bool {
		stats, statsErr := bus.SessionMemoryStats(context.Background())
		return statsErr == nil && stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0 && invoker.completedTurnCalls() >= 2
	})
	if err := worker.Stop(context.Background()); err != nil {
		t.Fatalf("worker.Stop() error = %v", err)
	}
	if turnResult.ExportID == "" || boundaryResult.ExportID == "" {
		t.Fatal("capture did not return stable export identities")
	}
	if invoker.completedTurnCalls() != 2 || invoker.boundaryCalls() != 1 {
		t.Fatalf("native derivation calls = turns %d boundaries %d, want turn retry + boundary", invoker.completedTurnCalls(), invoker.boundaryCalls())
	}

	scope, err := resolver.Resolve(locator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve(personal) error = %v", err)
	}
	personal, err := provider.SearchDerived(context.Background(), sessionmemory.DerivedSearchRequest{Scope: scope, Query: "release", Limit: 10})
	if err != nil {
		t.Fatalf("provider.SearchDerived(personal) error = %v", err)
	}
	if len(personal.Results) == 0 || personal.Scope != scope {
		t.Fatalf("personal search results = %+v, want exact-scope native results", personal.Results)
	}

	groupLocator := baldatelegram.NewLocator(-100, 42)
	groupScope, err := resolver.Resolve(groupLocator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve(group) error = %v", err)
	}
	group, err := provider.SearchDerived(context.Background(), sessionmemory.DerivedSearchRequest{Scope: groupScope, Query: "release", Limit: 10})
	if err != nil {
		t.Fatalf("provider.SearchDerived(group) error = %v", err)
	}
	if len(group.Results) != 0 {
		t.Fatalf("group search leaked personal results = %+v", group.Results)
	}
}

type nativeE2EInvoker struct {
	mu         sync.Mutex
	turnCalls  int
	boundaries int
}

func (i *nativeE2EInvoker) Invoke(_ context.Context, invocation sessionmemoryapp.StructuredInvocation) ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch invocation.Stage {
	case string(sessionmemory.OperationStageAtoms):
		i.turnCalls++
		if i.turnCalls == 1 {
			return nil, sessionmemory.RetryableError(sessionmemory.CodeUnavailable, "scripted transient failure", nil)
		}
		return []byte(`{"output":[{"category":"fact","text":"release checklist","relation":"new"}]}`), nil
	case string(sessionmemory.OperationStageScenarios):
		i.boundaries++
		return []byte(`{"output":[]}`), nil
	case string(sessionmemory.OperationStageProfile):
		var request sessionmemory.ProfileSynthesisRequest
		if err := json.Unmarshal(invocation.InputJSON, &request); err != nil || len(request.View.Atoms) == 0 {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "scripted profile input is invalid", nil)
		}
		ref := request.View.Atoms[0].Meta
		return []byte(`{"output":{"summary":"release profile","atoms":[{"item_id":"` + ref.ItemID + `","revision_id":"` + ref.RevisionID + `"}],"scenarios":[]}}`), nil
	default:
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "scripted stage is unknown", nil)
	}
}

func (i *nativeE2EInvoker) Close(context.Context) error { return nil }

func (i *nativeE2EInvoker) completedTurnCalls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.turnCalls
}

func (i *nativeE2EInvoker) boundaryCalls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.boundaries
}
