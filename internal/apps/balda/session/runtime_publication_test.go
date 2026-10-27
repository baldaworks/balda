package session

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	adksession "google.golang.org/adk/v2/session"
)

func TestPublishRuntimeCommitsBeforeSwapAndDrainsPredecessor(t *testing.T) {
	var closes atomic.Int32
	incumbent, candidate := publicationSlots(t, &closes)
	lease, err := incumbent.acquire()
	if err != nil {
		t.Fatal(err)
	}
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	store := &publicationStore{}
	store.beforeFinalize = func(outcome baldastate.RuntimeOperationOutcome) {
		if outcome != baldastate.RuntimeOperationOutcomeContextPreserved {
			return
		}
		if got := topic.currentRuntime(); got != incumbent {
			t.Fatal("runtime swapped before durable finalization")
		}
	}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if err != nil || !published {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if terminal.State != baldastate.RuntimeOperationTerminal || terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextPreserved) {
		t.Fatalf("terminal operation = %+v", terminal)
	}
	if got := store.callOrder(); strings.Join(got, ",") != "begin,applying,finalize:context_preserved" {
		t.Fatalf("store order = %v", got)
	}
	if closes.Load() != 0 {
		t.Fatal("predecessor closed while its lease or shared successor remained live")
	}
	current, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatal(err)
	}
	if current.Generation() != 2 || current.EffectiveModel() != "model-2" {
		t.Fatalf("current runtime = generation %d, model %q", current.Generation(), current.EffectiveModel())
	}
	current.Release()
	lease.Release()
	if closes.Load() != 0 {
		t.Fatal("predecessor drain closed shared successor resources")
	}
	if err := candidate.abortUnpublished(t.Context()); err != nil {
		t.Fatalf("abort after ownership transfer = %v", err)
	}
	if closes.Load() != 0 {
		t.Fatal("abort after publication closed the active runtime")
	}
	if err := topic.DrainRuntime(t.Context()); err != nil {
		t.Fatal(err)
	}
	if closes.Load() != 1 {
		t.Fatalf("aggregate close count = %d, want 1", closes.Load())
	}
}

func TestPublishRuntimeUsesGuardedSQLiteCommit(t *testing.T) {
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	if err := provider.Sessions().Upsert(t.Context(), baldastate.SessionRecord{
		SessionID: "session", UserID: "user", ChannelType: baldastate.ChannelTypeTelegram,
		AddressKey: "session:0", AddressJSON: `{}`, AgentName: "agent",
		RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding,
		ArchiveID: publicationArchiveID, RuntimeGeneration: 1, EffectiveModel: publicationOriginalModel,
		RuntimeOutcome: publicationOutcome, Status: baldastate.SessionStatusActive,
	}); err != nil {
		t.Fatal(err)
	}
	incumbent, candidate := publicationSlots(t, nil)
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	manager := publicationManager(topic, provider.RuntimeOperations())

	terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if err != nil || !published || terminal.State != baldastate.RuntimeOperationTerminal {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	committed, found, err := provider.Sessions().GetBySessionID(t.Context(), "session")
	if err != nil || !found {
		t.Fatalf("GetBySessionID() = %+v, %v, %v", committed, found, err)
	}
	if committed.RuntimeGeneration != 2 || committed.ArchiveID != publicationArchiveID ||
		committed.EffectiveModel != "model-2" || committed.RuntimeOutcome != publicationOutcome {
		t.Fatalf("committed runtime = %+v", committed)
	}
}

func TestPublishApplyingModelRuntimeCommitsAndSwapsOnce(t *testing.T) {
	incumbent, candidate := publicationSlots(t, nil)
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	intent := publicationIntent()
	intent.State = baldastate.RuntimeOperationApplying
	store := &publicationStore{operation: intent}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishApplyingModelRuntime(
		t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved,
	)
	if err != nil || !published || terminal.State != baldastate.RuntimeOperationTerminal {
		t.Fatalf("publishApplyingModelRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if got := topic.currentRuntime(); got != candidate {
		t.Fatal("successor is not current")
	}
	if got := store.callOrder(); strings.Join(got, ",") != "finalize:context_preserved" {
		t.Fatalf("store order = %v", got)
	}
	terminal, published, err = manager.publishApplyingModelRuntime(
		t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved,
	)
	if err != nil || published || terminal.State != baldastate.RuntimeOperationTerminal {
		t.Fatalf("replay = %+v, %v, %v", terminal, published, err)
	}
}

func TestPublishApplyingModelRuntimeFailuresRemainUnresolvedAndFenced(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*TopicSession, *RuntimeSlot, *RuntimeSlot, *publicationStore, *baldastate.RuntimeOperationRecord)
	}{
		{name: "candidate mismatch", setup: func(_ *TopicSession, _ *RuntimeSlot, candidate *RuntimeSlot, _ *publicationStore, _ *baldastate.RuntimeOperationRecord) {
			candidate.value.runtime.RuntimeSnapshotID = "mismatch"
		}},
		{name: "stale guard", setup: func(topic *TopicSession, _ *RuntimeSlot, _ *RuntimeSlot, _ *publicationStore, _ *baldastate.RuntimeOperationRecord) {
			topic.runtime = newPublishedRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
		}},
		{name: "archive guard", setup: func(_ *TopicSession, _ *RuntimeSlot, candidate *RuntimeSlot, store *publicationStore, intent *baldastate.RuntimeOperationRecord) {
			intent.ExpectedArchiveID = "stale-archive"
			store.operation.ExpectedArchiveID = intent.ExpectedArchiveID
			candidate.value.archiveID = publicationArchiveID
		}},
		{name: "generation guard", setup: func(_ *TopicSession, _ *RuntimeSlot, candidate *RuntimeSlot, store *publicationStore, intent *baldastate.RuntimeOperationRecord) {
			intent.ExpectedGeneration = 7
			store.operation.ExpectedGeneration = intent.ExpectedGeneration
			candidate.value.generation = 8
		}},
		{name: "claim failure", setup: func(_ *TopicSession, _ *RuntimeSlot, candidate *RuntimeSlot, _ *publicationStore, _ *baldastate.RuntimeOperationRecord) {
			candidate.markPublished()
		}},
		{name: "finalize conflict", setup: func(_ *TopicSession, _ *RuntimeSlot, _ *RuntimeSlot, store *publicationStore, _ *baldastate.RuntimeOperationRecord) {
			store.publishErr = baldastate.ErrRuntimeOperationConflict
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var closes atomic.Int32
			incumbent := newRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
			candidate := newRuntimeSlot(runtimeSlotConfig{
				runtime: publicationRuntime(func() error { closes.Add(1); return nil }), archiveID: publicationArchiveID,
				generation: 2, effectiveModel: "model-2", outcome: publicationOutcome,
			})
			intent := publicationIntent()
			intent.State = baldastate.RuntimeOperationApplying
			store := &publicationStore{operation: intent}
			topic := &TopicSession{sessionID: "session", runtime: incumbent}
			topic.runtime.markPublished()
			test.setup(topic, incumbent, candidate, store, &intent)
			manager := publicationManager(topic, store)

			got, published, err := manager.publishApplyingModelRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
			if !errors.Is(err, ErrRuntimeRecoveryRequired) || published {
				t.Fatalf("publishApplyingModelRuntime() = %+v, %v, %v", got, published, err)
			}
			if store.operation.State != baldastate.RuntimeOperationApplying || store.operation.TerminalAt != nil {
				t.Fatalf("operation terminalized = %+v", store.operation)
			}
			if strings.Contains(strings.Join(store.callOrder(), ","), "finalize:conflict") {
				t.Fatalf("local conflict was terminalized: %v", store.callOrder())
			}
			if _, acquireErr := topic.AcquireRuntime(); !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
				t.Fatalf("AcquireRuntime() = %v", acquireErr)
			}
			if test.name != "claim failure" && closes.Load() != 1 {
				t.Fatalf("candidate close count = %d, want 1", closes.Load())
			}
		})
	}
}

func TestPublishApplyingModelRuntimeCandidateCoherenceFailureFences(t *testing.T) {
	for _, test := range []struct {
		name      string
		candidate func(*RuntimeSlot) *RuntimeSlot
	}{
		{name: "missing", candidate: func(*RuntimeSlot) *RuntimeSlot { return nil }},
		{name: "incumbent reused", candidate: func(incumbent *RuntimeSlot) *RuntimeSlot { return incumbent }},
	} {
		t.Run(test.name, func(t *testing.T) {
			incumbent := newPublishedRuntimeSlot(runtimeSlotConfig{
				runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
			})
			intent := publicationIntent()
			intent.State = baldastate.RuntimeOperationApplying
			store := &publicationStore{operation: intent}
			topic := &TopicSession{sessionID: "session", runtime: incumbent}
			manager := publicationManager(topic, store)

			_, published, err := manager.publishApplyingModelRuntime(
				t.Context(), incumbent, test.candidate(incumbent), intent, baldastate.RuntimeOperationOutcomeContextPreserved,
			)
			if !errors.Is(err, ErrRuntimeRecoveryRequired) || published || store.operation.State != baldastate.RuntimeOperationApplying {
				t.Fatalf("publication = %v, %v; operation = %+v", published, err, store.operation)
			}
			if _, acquireErr := topic.AcquireRuntime(); !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
				t.Fatalf("AcquireRuntime() = %v", acquireErr)
			}
		})
	}
}

func TestRuntimePublicationIdentityGuardIsModelOnly(t *testing.T) {
	incumbentService := adksession.InMemoryService()
	candidateService := adksession.InMemoryService()
	incumbentSession, err := incumbentService.Create(t.Context(), &adksession.CreateRequest{AppName: "old-app", UserID: "old-user", SessionID: "old-session"})
	if err != nil {
		t.Fatal(err)
	}
	candidateSession, err := candidateService.Create(t.Context(), &adksession.CreateRequest{AppName: "new-app", UserID: "new-user", SessionID: "new-session"})
	if err != nil {
		t.Fatal(err)
	}
	incumbent := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{SessionSvc: incumbentService, AppName: "old-app", RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
		session: incumbentSession.Session, archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
	})
	candidate := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{SessionSvc: candidateService, AppName: "new-app", RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
		session: candidateSession.Session, archiveID: publicationArchiveID, generation: 2, effectiveModel: publicationOriginalModel,
		outcome: string(baldastate.RuntimeOperationOutcomeContextReset),
	})
	intent := publicationIntent()
	intent.Kind = string(baldastate.RuntimeOperationKindLifetime)
	intent.TargetModel = publicationOriginalModel
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	manager := publicationManager(topic, &publicationStore{})
	if _, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextReset); err != nil || !published {
		t.Fatalf("generic lifetime publication = %v, %v", published, err)
	}

	modelIntent := publicationIntent()
	modelIntent.State = baldastate.RuntimeOperationApplying
	modelStore := &publicationStore{operation: modelIntent}
	modelTopic := &TopicSession{sessionID: "session", runtime: incumbent}
	modelManager := publicationManager(modelTopic, modelStore)
	modelCandidate := newRuntimeSlot(runtimeSlotConfig{
		runtime: &BuiltRuntime{SessionSvc: candidateService, AppName: "new-app", RuntimeSnapshotID: "snapshot", ProviderBindingJSON: publicationBinding},
		session: candidateSession.Session, archiveID: publicationArchiveID, generation: 2, effectiveModel: "model-2", outcome: publicationOutcome,
	})
	if _, published, err := modelManager.publishApplyingModelRuntime(t.Context(), incumbent, modelCandidate, modelIntent, baldastate.RuntimeOperationOutcomeContextPreserved); !errors.Is(err, ErrRuntimeRecoveryRequired) || published {
		t.Fatalf("model identity mismatch = %v, %v", published, err)
	}
	if modelStore.operation.State != baldastate.RuntimeOperationApplying {
		t.Fatalf("model operation = %+v", modelStore.operation)
	}
}

func TestPublishRuntimeGuardConflictLeavesCandidateCallerOwned(t *testing.T) {
	var candidateCloses atomic.Int32
	stale := newRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
	actual := newRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
	candidate := newRuntimeSlot(runtimeSlotConfig{
		runtime:   publicationRuntime(func() error { candidateCloses.Add(1); return nil }),
		archiveID: publicationArchiveID, generation: 2, effectiveModel: "model-2", outcome: publicationOutcome,
	})
	topic := &TopicSession{sessionID: "session", runtime: actual}
	store := &publicationStore{}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishRuntime(t.Context(), stale, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimePublicationConflict) || published {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if terminal.Outcome != string(baldastate.RuntimeOperationOutcomeConflict) || terminal.ReasonCode != string(baldastate.RuntimeOperationReasonGuardConflict) {
		t.Fatalf("terminal conflict = %+v", terminal)
	}
	if got := topic.currentRuntime(); got != actual {
		t.Fatal("guard conflict replaced the incumbent")
	}
	if got := store.callOrder(); strings.Join(got, ",") != "begin,finalize:conflict" {
		t.Fatalf("store order = %v", got)
	}
	for range 8 {
		if err := candidate.abortUnpublished(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidateCloses.Load())
	}
}

func TestPublishRuntimeArchiveAndGenerationGuardsFailClosed(t *testing.T) {
	tests := []struct {
		name               string
		expectedArchiveID  string
		expectedGeneration uint64
	}{
		{name: "archive", expectedArchiveID: "stale-archive", expectedGeneration: 1},
		{name: "generation", expectedArchiveID: publicationArchiveID, expectedGeneration: 7},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incumbent := newRuntimeSlot(runtimeSlotConfig{
				runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
			})
			candidate := newRuntimeSlot(runtimeSlotConfig{
				runtime: publicationRuntime(nil), archiveID: test.expectedArchiveID,
				generation: test.expectedGeneration + 1, effectiveModel: "model-2", outcome: publicationOutcome,
			})
			topic := &TopicSession{sessionID: "session", runtime: incumbent}
			store := &publicationStore{}
			manager := publicationManager(topic, store)
			intent := publicationIntent()
			intent.ExpectedArchiveID = test.expectedArchiveID
			intent.ExpectedGeneration = test.expectedGeneration
			intent.TargetArchiveID = test.expectedArchiveID

			terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
			if !errors.Is(err, ErrRuntimePublicationConflict) || published || terminal.Outcome != string(baldastate.RuntimeOperationOutcomeConflict) {
				t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
			}
			if topic.currentRuntime() != incumbent {
				t.Fatal("stale guard replaced incumbent")
			}
		})
	}
}

func TestPublishRuntimeRejectsCandidateAlreadyInUse(t *testing.T) {
	incumbent, candidate := publicationSlots(t, nil)
	lease, err := candidate.acquire()
	if err != nil {
		t.Fatal(err)
	}
	defer lease.Release()
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	store := &publicationStore{}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimePublicationConflict) || published || terminal.Outcome != string(baldastate.RuntimeOperationOutcomeConflict) {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if got := store.callOrder(); strings.Join(got, ",") != "begin,finalize:conflict" {
		t.Fatalf("store order = %v", got)
	}
}

func TestPublishRuntimeUncertainFinalizeFencesAdmission(t *testing.T) {
	var candidateCloses atomic.Int32
	incumbent := newRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
	candidate := newRuntimeSlot(runtimeSlotConfig{
		runtime:   publicationRuntime(func() error { candidateCloses.Add(1); return errors.New("provider-secret-canary") }),
		archiveID: publicationArchiveID, generation: 2, effectiveModel: "model-2", outcome: publicationOutcome,
	})
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	store := &publicationStore{publishErr: errors.New("database-secret-canary")}
	manager := publicationManager(topic, store)

	_, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || err.Error() != ErrRuntimeRecoveryRequired.Error() || published {
		t.Fatalf("publishRuntime() = %v, %v", published, err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
	}
	if topic.GetRunner() != nil {
		t.Fatal("raw runner escaped the recovery fence")
	}
	if err := candidate.abortUnpublished(t.Context()); !errors.Is(err, ErrRuntimeCloseFailed) || err.Error() != ErrRuntimeCloseFailed.Error() {
		t.Fatalf("candidate abort = %v", err)
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("candidate close count = %d, want 1", candidateCloses.Load())
	}
}

func TestPublishRuntimePreloadedApplyingOnlyFencesAdmission(t *testing.T) {
	var candidateCloses atomic.Int32
	incumbent := newRuntimeSlot(runtimeSlotConfig{
		runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
	})
	candidate := newRuntimeSlot(runtimeSlotConfig{
		runtime:   publicationRuntime(func() error { candidateCloses.Add(1); return nil }),
		archiveID: publicationArchiveID, generation: 2, effectiveModel: "model-2", outcome: publicationOutcome,
	})
	intent := publicationIntent()
	applying := intent
	applying.State = baldastate.RuntimeOperationApplying
	store := &publicationStore{operation: applying}
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	manager := publicationManager(topic, store)

	got, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || published || got.State != baldastate.RuntimeOperationApplying {
		t.Fatalf("publishRuntime() = %+v, %v, %v", got, published, err)
	}
	if calls := store.callOrder(); strings.Join(calls, ",") != publicationBeginCall {
		t.Fatalf("store calls = %v, want Begin only", calls)
	}
	if topic.currentRuntime() != incumbent {
		t.Fatal("applying replay swapped the runtime")
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
	}
	if err := candidate.abortUnpublished(t.Context()); err != nil {
		t.Fatal(err)
	}
	if candidateCloses.Load() != 1 {
		t.Fatalf("candidate close count = %d, want caller-owned candidate", candidateCloses.Load())
	}
}

func TestPublishRuntimeTerminalSuccessReplayRequiresCommittedCurrentSlot(t *testing.T) {
	tests := []struct {
		name          string
		currentIsNext bool
		wantErr       error
	}{
		{name: "current", currentIsNext: true},
		{name: "stale", wantErr: ErrRuntimeRecoveryRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			incumbent, candidate := publicationSlots(t, nil)
			current := incumbent
			if test.currentIsNext {
				current = candidate
			}
			topic := &TopicSession{sessionID: "session", runtime: current}
			intent := publicationIntent()
			store := &publicationStore{operation: terminalPublicationOperation(intent, baldastate.RuntimeOperationOutcomeContextPreserved, "")}
			manager := publicationManager(topic, store)

			terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
			if !errors.Is(err, test.wantErr) || published || terminal.Outcome != publicationOutcome {
				t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
			}
			if calls := store.callOrder(); strings.Join(calls, ",") != publicationBeginCall {
				t.Fatalf("store calls = %v, want Begin only", calls)
			}
			lease, acquireErr := topic.AcquireRuntime()
			if test.wantErr == nil && acquireErr != nil {
				t.Fatalf("AcquireRuntime() = %v", acquireErr)
			}
			if lease != nil {
				lease.Release()
			}
			if test.wantErr != nil && !errors.Is(acquireErr, ErrRuntimeRecoveryRequired) {
				t.Fatalf("AcquireRuntime() = %v, want recovery fence", acquireErr)
			}
		})
	}
}

func TestPublishRuntimeTerminalConflictReplaysConflictSemantic(t *testing.T) {
	incumbent, candidate := publicationSlots(t, nil)
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	intent := publicationIntent()
	store := &publicationStore{operation: terminalPublicationOperation(
		intent,
		baldastate.RuntimeOperationOutcomeConflict,
		baldastate.RuntimeOperationReasonGuardConflict,
	)}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimePublicationConflict) || published ||
		terminal.Outcome != string(baldastate.RuntimeOperationOutcomeConflict) ||
		terminal.ReasonCode != string(baldastate.RuntimeOperationReasonGuardConflict) {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if calls := store.callOrder(); strings.Join(calls, ",") != publicationBeginCall {
		t.Fatalf("store calls = %v, want Begin only", calls)
	}
	lease, err := topic.AcquireRuntime()
	if err != nil {
		t.Fatalf("conflict replay fenced healthy incumbent: %v", err)
	}
	lease.Release()
}

func TestPublishRuntimePostCommitMismatchFencesAdmission(t *testing.T) {
	incumbent, candidate := publicationSlots(t, nil)
	alternate := newRuntimeSlot(runtimeSlotConfig{runtime: publicationRuntime(nil), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel})
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	store := &publicationStore{}
	store.beforeFinalize = func(outcome baldastate.RuntimeOperationOutcome) {
		if outcome != baldastate.RuntimeOperationOutcomeContextPreserved {
			return
		}
		topic.runtimeMu.Lock()
		topic.runtime = alternate
		topic.runtimeMu.Unlock()
	}
	manager := publicationManager(topic, store)

	terminal, published, err := manager.publishRuntime(t.Context(), incumbent, candidate, publicationIntent(), baldastate.RuntimeOperationOutcomeContextPreserved)
	if !errors.Is(err, ErrRuntimeRecoveryRequired) || published || terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextPreserved) {
		t.Fatalf("publishRuntime() = %+v, %v, %v", terminal, published, err)
	}
	if _, err := topic.AcquireRuntime(); !errors.Is(err, ErrRuntimeRecoveryRequired) {
		t.Fatalf("AcquireRuntime() = %v, want recovery fence", err)
	}
	if err := candidate.abortUnpublished(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestPublishRuntimeConcurrentRedeliveryPublishesOnce(t *testing.T) {
	incumbent, candidate := publicationSlots(t, nil)
	topic := &TopicSession{sessionID: "session", runtime: incumbent}
	store := &publicationStore{}
	manager := publicationManager(topic, store)
	intent := publicationIntent()

	const callers = 32
	var published atomic.Int32
	errs := make(chan error, callers)
	var wg sync.WaitGroup
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			terminal, didPublish, err := manager.publishRuntime(t.Context(), incumbent, candidate, intent, baldastate.RuntimeOperationOutcomeContextPreserved)
			if err == nil && terminal.Outcome != string(baldastate.RuntimeOperationOutcomeContextPreserved) {
				err = errors.New("redelivery returned a different semantic result")
			}
			if didPublish {
				published.Add(1)
			}
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if published.Load() != 1 {
		t.Fatalf("publication count = %d, want 1", published.Load())
	}
	if got := topic.currentRuntime(); got != candidate {
		t.Fatal("successor is not current")
	}
	if store.successfulFinalizations() != 1 {
		t.Fatalf("successful durable finalizations = %d, want 1", store.successfulFinalizations())
	}
}

func publicationManager(topic *TopicSession, store baldastate.RuntimeOperationStore) *Manager {
	if topic.runtime != nil {
		topic.runtime.markPublished()
	}
	return &Manager{
		sessions:          map[string]*TopicSession{topic.sessionID: topic},
		operations:        make(map[string]*sessionOperation),
		runtimeOperations: store,
	}
}

func publicationSlots(t *testing.T, closes *atomic.Int32) (*RuntimeSlot, *RuntimeSlot) {
	t.Helper()
	var closeFn func() error
	if closes != nil {
		closeFn = func() error { closes.Add(1); return nil }
	}
	incumbent := newRuntimeSlot(runtimeSlotConfig{
		runtime: publicationRuntime(closeFn), archiveID: publicationArchiveID, generation: 1, effectiveModel: publicationOriginalModel,
	})
	candidate, err := incumbent.sharedSuccessor(2, "model-2", publicationOutcome)
	if err != nil {
		t.Fatal(err)
	}
	return incumbent, candidate
}

func publicationRuntime(closeFn func() error) *BuiltRuntime {
	return &BuiltRuntime{
		RuntimeSnapshotID:   "snapshot",
		ProviderBindingJSON: publicationBinding,
		Close:               closeFn,
	}
}

func publicationIntent() baldastate.RuntimeOperationRecord {
	now := time.Now().UTC()
	return baldastate.RuntimeOperationRecord{
		OperationID: "operation", SessionID: "session", InputHash: strings.Repeat("a", 64),
		Kind: string(baldastate.RuntimeOperationKindModel), State: baldastate.RuntimeOperationPrepared,
		ExpectedArchiveID: publicationArchiveID, ExpectedGeneration: 1,
		TargetSnapshotID: "snapshot", TargetProviderBindingJSON: publicationBinding,
		TargetModel: "model-2", CreatedAt: now, UpdatedAt: now,
	}
}

func terminalPublicationOperation(
	intent baldastate.RuntimeOperationRecord,
	outcome baldastate.RuntimeOperationOutcome,
	reason baldastate.RuntimeOperationReason,
) baldastate.RuntimeOperationRecord {
	terminalAt := intent.UpdatedAt.Add(time.Second)
	intent.State = baldastate.RuntimeOperationTerminal
	intent.Outcome = string(outcome)
	intent.ReasonCode = string(reason)
	intent.UpdatedAt = terminalAt
	intent.TerminalAt = &terminalAt
	return intent
}

const (
	publicationArchiveID     = "runtime-publication-archive"
	publicationBeginCall     = "begin"
	publicationBinding       = `{"RootProviderID":"provider","Entries":[{"ProviderID":"provider","Source":"managed","RevisionID":"revision"}],"Reproducible":true,"Warnings":null}`
	publicationOutcome       = string(baldastate.RuntimeOperationOutcomeContextPreserved)
	publicationOriginalModel = "model-1"
)

type publicationStore struct {
	mu             sync.Mutex
	operation      baldastate.RuntimeOperationRecord
	calls          []string
	publishErr     error
	beforeFinalize func(baldastate.RuntimeOperationOutcome)
	finalizations  int
}

func (s *publicationStore) Begin(_ context.Context, intent baldastate.RuntimeOperationRecord) (baldastate.RuntimeOperationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "begin")
	if s.operation.OperationID == "" {
		s.operation = intent
		return intent, nil
	}
	if s.operation.OperationID != intent.OperationID || s.operation.InputHash != intent.InputHash || s.operation.SessionID != intent.SessionID {
		return baldastate.RuntimeOperationRecord{}, baldastate.ErrRuntimeOperationConflict
	}
	return s.operation, nil
}

func (s *publicationStore) MarkApplying(_ context.Context, operationID, inputHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "applying")
	if s.operation.OperationID != operationID || s.operation.InputHash != inputHash || s.operation.State == baldastate.RuntimeOperationTerminal {
		return baldastate.ErrRuntimeOperationConflict
	}
	s.operation.State = baldastate.RuntimeOperationApplying
	return nil
}

func (s *publicationStore) Finalize(_ context.Context, final baldastate.RuntimeOperationFinalization) (baldastate.RuntimeOperationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, "finalize:"+string(final.Outcome))
	if s.beforeFinalize != nil {
		s.beforeFinalize(final.Outcome)
	}
	if final.Outcome == baldastate.RuntimeOperationOutcomeContextPreserved && s.publishErr != nil {
		return baldastate.RuntimeOperationRecord{}, s.publishErr
	}
	if s.operation.OperationID != final.OperationID || s.operation.InputHash != final.InputHash {
		return baldastate.RuntimeOperationRecord{}, baldastate.ErrRuntimeOperationConflict
	}
	if s.operation.State == baldastate.RuntimeOperationTerminal {
		if s.operation.Outcome != string(final.Outcome) || s.operation.ReasonCode != string(final.ReasonCode) {
			return baldastate.RuntimeOperationRecord{}, baldastate.ErrRuntimeOperationConflict
		}
		return s.operation, nil
	}
	s.operation.State = baldastate.RuntimeOperationTerminal
	s.operation.Outcome = string(final.Outcome)
	s.operation.ReasonCode = string(final.ReasonCode)
	s.operation.TerminalAt = &final.At
	if final.Outcome == baldastate.RuntimeOperationOutcomeContextPreserved || final.Outcome == baldastate.RuntimeOperationOutcomeContextReset {
		s.finalizations++
	}
	return s.operation, nil
}

func (s *publicationStore) Get(_ context.Context, operationID string) (baldastate.RuntimeOperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.operation, s.operation.OperationID == operationID, nil
}

func (s *publicationStore) GetUnresolvedBySession(_ context.Context, sessionID string) (baldastate.RuntimeOperationRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := s.operation.SessionID == sessionID && s.operation.State != baldastate.RuntimeOperationTerminal
	return s.operation, found, nil
}

func (*publicationStore) Prune(context.Context, time.Time, int) (int64, error) { return 0, nil }

func (s *publicationStore) callOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *publicationStore) successfulFinalizations() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizations
}
