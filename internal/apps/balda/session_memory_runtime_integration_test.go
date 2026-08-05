package balda

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	baldaeventbus "github.com/normahq/balda/internal/apps/balda/eventbus"
	natsbus "github.com/normahq/balda/internal/apps/balda/eventbus/nats"
	baldaexecution "github.com/normahq/balda/internal/apps/balda/execution"
	"github.com/normahq/balda/internal/apps/balda/memory"
	"github.com/normahq/balda/internal/apps/balda/sessionmemoryapp"
	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
	"github.com/rs/zerolog"
	"go.uber.org/fx/fxtest"
)

func TestSessionMemoryRuntimePersistsPubAckedExportsAcrossRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stateDir := t.TempDir()
	locator := baldatelegram.NewLocator(123, 0)
	resolver := sessionmemoryapp.NewScopeResolver(map[string]sessionmemoryapp.ScopeClassifier{
		baldatelegram.ChannelType: baldatelegram.ClassifyLocatorScope,
	})
	scope, err := resolver.Resolve(locator)
	if err != nil {
		t.Fatalf("ScopeResolver.Resolve() error = %v", err)
	}
	session := sessionmemory.SessionRef{
		SessionID:         "session-7",
		AgentSessionID:    "adk-7",
		LineageID:         "lineage-7",
		PreviousSessionID: "session-6",
	}
	completedAt := time.Date(2026, 8, 4, 5, 6, 7, 0, time.UTC)
	sourceTurnID := "telegram:message:9"

	stateA, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(boot A) error = %v", err)
	}
	t.Cleanup(func() { _ = stateA.Close() })
	factsA := memory.NewStore(stateA.AppKV(), "", true)
	wantFacts, err := factsA.Remember(ctx, "global fact remains independent")
	if err != nil {
		t.Fatalf("global memory Remember() error = %v", err)
	}

	busA := startSessionMemoryRuntimeBus(t, stateDir)
	turnCapture := sessionmemoryapp.NewTurnCapture(busA.SessionMemoryExportPublisher(), resolver)
	turnResult, err := turnCapture.Capture(ctx, sessionmemoryapp.CaptureRequest{
		UserText:          "remember the durable release checklist",
		AssistantText:     "the durable release checklist is ready",
		Locator:           locator,
		SessionID:         session.SessionID,
		AgentSessionID:    session.AgentSessionID,
		LineageID:         session.LineageID,
		PreviousSessionID: session.PreviousSessionID,
		SourceTurnID:      sourceTurnID,
		CompletedAt:       completedAt,
	})
	if err != nil {
		t.Fatalf("TurnCapture.Capture() error = %v", err)
	}
	boundaryCapture := sessionmemoryapp.NewBoundaryCapture(busA.SessionMemoryExportPublisher(), resolver)
	boundaryResult, err := boundaryCapture.Capture(ctx, sessionmemoryapp.BoundaryCaptureRequest{
		Locator:           locator,
		SessionID:         session.SessionID,
		AgentSessionID:    session.AgentSessionID,
		LineageID:         session.LineageID,
		PreviousSessionID: session.PreviousSessionID,
		TransitionID:      "rotation-1",
		Reason:            sessionmemory.BoundaryReasonRotation,
		OccurredAt:        completedAt.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("BoundaryCapture.Capture() error = %v", err)
	}
	if turnResult.ExportID == "" || boundaryResult.ExportID == "" {
		t.Fatal("capture returned an empty export identity")
	}
	stats, err := waitForRuntimeMemoryStats(ctx, busA, func(stats natsbus.SessionMemoryStats) bool {
		return stats.Messages == 2 && stats.Pending == 2 && stats.Acknowledging == 0
	})
	if err != nil {
		t.Fatalf("boot A PubAcked backlog did not settle: stats=%+v error=%v", stats, err)
	}
	if err := busA.Drain(ctx); err != nil {
		t.Fatalf("Bus.Drain(boot A) error = %v", err)
	}
	if err := stateA.Close(); err != nil {
		t.Fatalf("state provider Close(boot A) error = %v", err)
	}

	stateB, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(boot B) error = %v", err)
	}
	t.Cleanup(func() { _ = stateB.Close() })
	busB := startSessionMemoryRuntimeBus(t, stateDir)
	invoker := newRuntimeDurabilityInvoker()
	deriver, err := sessionmemoryapp.NewDeriver(invoker)
	if err != nil {
		t.Fatalf("NewDeriver() error = %v", err)
	}
	provider, err := sessionmemoryapp.NewNativeProvider(stateB.SessionMemoryStore(), deriver, invoker)
	if err != nil {
		t.Fatalf("NewNativeProvider() error = %v", err)
	}
	worker, err := sessionmemoryapp.NewWorker(busB.SessionMemoryTransport(), provider, sessionmemoryapp.Config{
		Enabled:          true,
		MaxAttempts:      2,
		RetryBaseDelay:   5 * time.Millisecond,
		RetryMaxDelay:    5 * time.Millisecond,
		ProgressInterval: 5 * time.Millisecond,
		FetchErrorDelay:  5 * time.Millisecond,
		ShutdownTimeout:  2 * time.Second,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewWorker() error = %v", err)
	}
	t.Cleanup(func() { _ = worker.Stop(context.Background()) })
	if err := worker.Start(ctx); err != nil {
		t.Fatalf("Worker.Start() error = %v", err)
	}
	stats, err = waitForRuntimeMemoryStats(ctx, busB, func(stats natsbus.SessionMemoryStats) bool {
		return stats.Messages == 0 && stats.Pending == 0 && stats.Acknowledging == 0 && invoker.boundaryCalls() == 1
	})
	if err != nil {
		t.Fatalf("boot B backlog did not drain: stats=%+v error=%v", stats, err)
	}

	boundary := invoker.lastBoundary()
	if boundary == nil {
		t.Fatal("boundary derivation did not observe a boundary")
	}
	if boundary.Scope != scope || boundary.Session != session || boundary.Reason != sessionmemory.BoundaryReasonRotation {
		t.Fatalf("boundary identity = scope %q session %q reason %q, want exact old identity", boundary.Scope.Key, boundary.Session.SessionID, boundary.Reason)
	}

	snapshotBeforeReplay, err := stateB.SessionMemoryStore().LoadScope(ctx, scope)
	if err != nil {
		t.Fatalf("SessionMemoryStore.LoadScope(before replay) error = %v", err)
	}
	if snapshotBeforeReplay.Scope != scope || len(snapshotBeforeReplay.Sources) != 1 || len(snapshotBeforeReplay.Atoms) != 1 || len(snapshotBeforeReplay.Profiles) != 1 {
		t.Fatalf("persisted native counts = sources %d atoms %d scenarios %d profiles %d scope %q", len(snapshotBeforeReplay.Sources), len(snapshotBeforeReplay.Atoms), len(snapshotBeforeReplay.Scenarios), len(snapshotBeforeReplay.Profiles), snapshotBeforeReplay.Scope.Key)
	}
	if snapshotBeforeReplay.Sources[0].Turn == nil || snapshotBeforeReplay.Sources[0].Turn.Session != session {
		t.Fatal("persisted source did not retain the old session identity")
	}
	atom := snapshotBeforeReplay.Atoms[0]
	if len(atom.Meta.Provenance.RawSources) != 1 || atom.Meta.Provenance.RawSources[0].ExportID != turnResult.ExportID {
		t.Fatalf("atom raw provenance count = %d, want one captured turn export", len(atom.Meta.Provenance.RawSources))
	}
	profile := snapshotBeforeReplay.Profiles[0]
	if len(profile.Meta.Provenance.ParentRevisions) != 1 || profile.Meta.Provenance.ParentRevisions[0] != (sessionmemory.RevisionRef{ItemID: atom.Meta.ItemID, RevisionID: atom.Meta.RevisionID}) {
		t.Fatalf("profile parent provenance count = %d, want one atom revision", len(profile.Meta.Provenance.ParentRevisions))
	}
	search, err := provider.SearchDerived(ctx, sessionmemory.DerivedSearchRequest{Scope: scope, Query: "durable", Limit: 10})
	if err != nil {
		t.Fatalf("NativeProvider.SearchDerived() error = %v", err)
	}
	if search.Scope != scope || len(search.Results) == 0 {
		t.Fatalf("native search = scope %q results %d, want exact-scope results", search.Scope.Key, len(search.Results))
	}

	turn, err := sessionmemory.NewTurn(scope, session, sourceTurnID, completedAt, "remember the durable release checklist", "the durable release checklist is ready")
	if err != nil {
		t.Fatalf("NewTurn(replay) error = %v", err)
	}
	atomCallsBeforeReplay := invoker.atomCallsCount()
	if err := provider.SyncTurn(ctx, turn); err != nil {
		t.Fatalf("NativeProvider.SyncTurn(replay) error = %v", err)
	}
	snapshotAfterReplay, err := stateB.SessionMemoryStore().LoadScope(ctx, scope)
	if err != nil {
		t.Fatalf("SessionMemoryStore.LoadScope(after replay) error = %v", err)
	}
	if atomCallsAfterReplay := invoker.atomCallsCount(); atomCallsAfterReplay != atomCallsBeforeReplay {
		t.Fatalf("atom derivation calls after replay = %d, want %d", atomCallsAfterReplay, atomCallsBeforeReplay)
	}
	if !reflect.DeepEqual(snapshotAfterReplay, snapshotBeforeReplay) {
		t.Fatalf("scope snapshot changed after identical replay: version %d, want %d", snapshotAfterReplay.Version, snapshotBeforeReplay.Version)
	}
	factsAfterReplay, err := memory.NewStore(stateB.AppKV(), "", true).Snapshot(ctx)
	if err != nil {
		t.Fatalf("global memory Snapshot(boot B) error = %v", err)
	}
	if factsAfterReplay != wantFacts {
		t.Fatalf("global memory snapshot changed during native processing: version %d, want %d", factsAfterReplay.Version, wantFacts.Version)
	}

	if err := worker.Stop(ctx); err != nil {
		t.Fatalf("Worker.Stop() error = %v", err)
	}
	if err := busB.Drain(ctx); err != nil {
		t.Fatalf("Bus.Drain(boot B) error = %v", err)
	}
	if err := stateB.Close(); err != nil {
		t.Fatalf("state provider Close(boot B) error = %v", err)
	}

	stateC, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(final reopen) error = %v", err)
	}
	defer func() { _ = stateC.Close() }()
	reopenedSnapshot, err := stateC.SessionMemoryStore().LoadScope(ctx, scope)
	if err != nil {
		t.Fatalf("SessionMemoryStore.LoadScope(final reopen) error = %v", err)
	}
	if !reflect.DeepEqual(reopenedSnapshot, snapshotAfterReplay) {
		t.Fatalf("reopened scope snapshot changed: version %d, want %d", reopenedSnapshot.Version, snapshotAfterReplay.Version)
	}
	reopenedSearch, err := stateC.SessionMemoryStore().Search(ctx, sessionmemory.DerivedSearchRequest{Scope: scope, Query: "durable", Limit: 10})
	if err != nil {
		t.Fatalf("SessionMemoryStore.Search(final reopen) error = %v", err)
	}
	if len(reopenedSearch) == 0 {
		t.Fatal("final reopened Store search returned no results")
	}
	reopenedFacts, err := memory.NewStore(stateC.AppKV(), "", true).Snapshot(ctx)
	if err != nil {
		t.Fatalf("global memory Snapshot(final reopen) error = %v", err)
	}
	if reopenedFacts != wantFacts {
		t.Fatalf("global memory snapshot changed after reopen: version %d, want %d", reopenedFacts.Version, wantFacts.Version)
	}
}

func TestSessionMemoryIngressOutboxRepublishesAfterRestartBeforePubAck(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stateDir := t.TempDir()
	now := time.Date(2026, time.August, 5, 13, 0, 0, 0, time.UTC)
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:ingress-restart", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-restart", AgentSessionID: "agent-restart"},
		"turn-restart", now, "remember restart", "restart is durable",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn() export error = %v", err)
	}

	stateA, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(boot A) error = %v", err)
	}
	publisherA, err := sessionmemoryapp.NewIngressOutboxPublisher(stateA.SessionMemoryIngressOutbox(), failingSessionMemoryExportPublisher{}, sessionmemoryapp.IngressOutboxConfig{
		Enabled: true, WorkerID: "boot-a", RetryBaseDelay: time.Millisecond, RetryMaxDelay: time.Millisecond,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewIngressOutboxPublisher(boot A) error = %v", err)
	}
	if err := publisherA.Publish(ctx, export); err != nil {
		t.Fatalf("Publish(boot A) error = %v", err)
	}
	if err := stateA.Close(); err != nil {
		t.Fatalf("state provider Close(boot A) error = %v", err)
	}

	time.Sleep(2 * time.Millisecond)
	stateB, err := openBaldaStateProvider(ctx, stateDir)
	if err != nil {
		t.Fatalf("openBaldaStateProvider(boot B) error = %v", err)
	}
	t.Cleanup(func() { _ = stateB.Close() })
	bus := startSessionMemoryRuntimeBus(t, stateDir)
	publisherB, err := sessionmemoryapp.NewIngressOutboxPublisher(stateB.SessionMemoryIngressOutbox(), bus.SessionMemoryExportPublisher(), sessionmemoryapp.IngressOutboxConfig{
		Enabled: true, WorkerID: "boot-b", RetryBaseDelay: time.Millisecond, RetryMaxDelay: time.Millisecond,
	}, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewIngressOutboxPublisher(boot B) error = %v", err)
	}
	if err := publisherB.Flush(ctx); err != nil {
		t.Fatalf("Flush(boot B) error = %v", err)
	}
	delivery, err := bus.SessionMemoryTransport().Fetch(ctx)
	if err != nil {
		t.Fatalf("Fetch(republished export) error = %v", err)
	}
	got, err := delivery.Export()
	if err != nil || got.ExportID() != export.ExportID() {
		t.Fatalf("republished export = %q, error %v; want %q", got.ExportID(), err, export.ExportID())
	}
	if err := delivery.Ack(ctx); err != nil {
		t.Fatalf("Ack(republished export) error = %v", err)
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, now)
	if err != nil {
		t.Fatalf("NewIngressRecord(replay) error = %v", err)
	}
	stored, created, err := stateB.SessionMemoryIngressOutbox().EnqueueSessionMemoryIngress(ctx, record)
	if err != nil || created || stored.State != sessionmemorycmd.IngressStatePublished {
		t.Fatalf("EnqueueSessionMemoryIngress(replay) = %#v, created %v, error %v", stored, created, err)
	}
}

type failingSessionMemoryExportPublisher struct{}

func (failingSessionMemoryExportPublisher) Publish(context.Context, sessionmemorycmd.Export) error {
	return fmt.Errorf("simulated process stop before JetStream PubAck")
}

func startSessionMemoryRuntimeBus(t *testing.T, stateDir string) *natsbus.Bus {
	t.Helper()
	bus, err := natsbus.NewBus(natsbus.Params{
		LC:        fxtest.NewLifecycle(t),
		Config:    baldaeventbus.Config{Embedded: true},
		Execution: baldaexecution.Config{Memory: baldaexecution.SessionMemoryConfig{Enabled: true, FetchWait: "20ms"}},
		StateDir:  stateDir,
		Logger:    zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("natsbus.NewBus() error = %v", err)
	}
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("Bus.Start() error = %v", err)
	}
	t.Cleanup(func() { _ = bus.Drain(context.Background()) })
	return bus
}

func waitForRuntimeMemoryStats(ctx context.Context, bus *natsbus.Bus, ready func(natsbus.SessionMemoryStats) bool) (natsbus.SessionMemoryStats, error) {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	var last natsbus.SessionMemoryStats
	for {
		stats, err := bus.SessionMemoryStats(ctx)
		if err != nil {
			return last, err
		}
		last = stats
		if ready(stats) {
			return stats, nil
		}
		select {
		case <-ctx.Done():
			return last, ctx.Err()
		case <-ticker.C:
		}
	}
}

type runtimeDurabilityInvoker struct {
	mu                sync.Mutex
	atomCalls         int
	boundaries        int
	lastBoundaryValue *sessionmemory.Boundary
}

func newRuntimeDurabilityInvoker() *runtimeDurabilityInvoker {
	return &runtimeDurabilityInvoker{}
}

func (i *runtimeDurabilityInvoker) Invoke(_ context.Context, invocation sessionmemoryapp.StructuredInvocation) ([]byte, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	switch invocation.Stage {
	case string(sessionmemory.OperationStageAtoms):
		i.atomCalls++
		return []byte(`{"output":[{"category":"fact","text":"durable release checklist","relation":"new"}]}`), nil
	case string(sessionmemory.OperationStageScenarios):
		var request sessionmemory.ScenarioSynthesisRequest
		if err := json.Unmarshal(invocation.InputJSON, &request); err != nil {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "scripted scenario input is invalid", nil)
		}
		boundary := request.Boundary
		i.lastBoundaryValue = &boundary
		i.boundaries++
		return []byte(`{"output":[]}`), nil
	case string(sessionmemory.OperationStageProfile):
		var request sessionmemory.ProfileSynthesisRequest
		if err := json.Unmarshal(invocation.InputJSON, &request); err != nil || len(request.View.Atoms) == 0 {
			return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, "scripted profile input is invalid", nil)
		}
		ref := request.View.Atoms[0].Meta
		return json.Marshal(map[string]any{"output": sessionmemory.ProfileCandidate{
			Disposition: sessionmemory.ProfileDispositionUpsert,
			Summary:     "durable release profile",
			Atoms:       []sessionmemory.RevisionRef{{ItemID: ref.ItemID, RevisionID: ref.RevisionID}},
		}})
	default:
		return nil, sessionmemory.PermanentError(sessionmemory.CodeInvalidDerived, fmt.Sprintf("scripted stage %q is unsupported", invocation.Stage), nil)
	}
}

func (i *runtimeDurabilityInvoker) Close(context.Context) error { return nil }

func (i *runtimeDurabilityInvoker) atomCallsCount() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.atomCalls
}

func (i *runtimeDurabilityInvoker) boundaryCalls() int {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.boundaries
}

func (i *runtimeDurabilityInvoker) lastBoundary() *sessionmemory.Boundary {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.lastBoundaryValue == nil {
		return nil
	}
	boundary := *i.lastBoundaryValue
	return &boundary
}
