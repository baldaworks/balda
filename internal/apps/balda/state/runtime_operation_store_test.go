package state

import (
	"database/sql"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const testRuntimeBinding = `{"RootProviderID":"provider-next","Entries":[{"ProviderID":"provider-next","Source":"managed","RevisionID":"revision-next"}],"Reproducible":true,"Warnings":null}`

const testRuntimeBindingNoncanonical = ` {
	"Warnings": null,
	"Reproducible": true,
	"Entries": [{"RevisionID":"revision-next", "Source":"managed", "ProviderID":"provider-next"}],
	"RootProviderID": "provider-next"
}`

const genericRuntimeMustNotWin = "generic-must-not-win"

const runtimeOperationTestStep = time.Second

func TestSQLiteRuntimeOperationStore(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "runtime-operations.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := provider.Close(); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}()
	checkRuntimeOperationStore(t, provider)
}

func TestSQLiteRuntimeOperationFinalizeRollsBackSessionUpdate(t *testing.T) {
	providerValue, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "runtime-operation-rollback.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = providerValue.Close() }()
	checkRuntimeOperationFinalizeRollback(t, providerValue)
}

func TestSQLiteRuntimeOperationFinalizeConcurrentGenericUpsert(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "runtime-operation-concurrent-upsert.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	checkRuntimeOperationFinalizeConcurrentGenericUpsert(t, provider)
}

func checkRuntimeOperationFinalizeRollback(t *testing.T, provider Provider) {
	t.Helper()
	putRuntimeSession(t, provider, "session-rollback", "archive-one", 4)
	intent := testRuntimeIntent("operation-rollback", "session-rollback", "9", time.Now().UTC())
	if _, err := provider.RuntimeOperations().Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	applying := markRuntimeOperationApplying(t, provider.RuntimeOperations(), intent)
	db, _, postgres := runtimeOperationTestSQL(provider)
	trigger := `CREATE TRIGGER runtime_operation_rollback_probe
		BEFORE UPDATE OF state ON balda_runtime_operations
		WHEN NEW.operation_id='operation-rollback' AND NEW.state='terminal'
		BEGIN SELECT RAISE(ABORT,'protected-trigger-canary'); END`
	if postgres {
		trigger = `CREATE FUNCTION runtime_operation_rollback_probe() RETURNS trigger LANGUAGE plpgsql AS $$
			BEGIN RAISE EXCEPTION 'protected-trigger-canary'; END $$;
			CREATE TRIGGER runtime_operation_rollback_probe BEFORE UPDATE OF state ON balda_runtime_operations
			FOR EACH ROW WHEN (NEW.operation_id='operation-rollback' AND NEW.state='terminal')
			EXECUTE FUNCTION runtime_operation_rollback_probe()`
	}
	if _, err := db.ExecContext(t.Context(), trigger); err != nil {
		t.Fatal(err)
	}
	_, err := provider.RuntimeOperations().Finalize(t.Context(), RuntimeOperationFinalization{
		OperationID: intent.OperationID, InputHash: intent.InputHash,
		Outcome: RuntimeOperationOutcomeContextPreserved, At: applying.UpdatedAt.Add(runtimeOperationTestStep),
	})
	if !errors.Is(err, ErrRuntimeOperationUnavailable) || strings.Contains(err.Error(), "protected-trigger-canary") {
		t.Fatalf("Finalize() error = %v, want redacted unavailable", err)
	}
	session, found, err := provider.Sessions().GetBySessionID(t.Context(), intent.SessionID)
	if err != nil || !found || session.ArchiveID != intent.ExpectedArchiveID ||
		session.RuntimeGeneration != intent.ExpectedGeneration || session.EffectiveModel != "model-old" {
		t.Fatalf("session after failed transaction = %+v, %t, %v", session, found, err)
	}
	operation, found, err := provider.RuntimeOperations().Get(t.Context(), intent.OperationID)
	if err != nil || !found || operation.State != RuntimeOperationApplying || operation.Outcome != "" {
		t.Fatalf("operation after failed transaction = %+v, %t, %v", operation, found, err)
	}
}

func checkRuntimeOperationFinalizeConcurrentGenericUpsert(t *testing.T, provider Provider) {
	t.Helper()
	putRuntimeSession(t, provider, "session-concurrent-upsert", "archive-one", 4)
	intent := testRuntimeIntent("operation-concurrent-upsert", "session-concurrent-upsert", "8", time.Now().UTC())
	if _, err := provider.RuntimeOperations().Begin(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	applying := markRuntimeOperationApplying(t, provider.RuntimeOperations(), intent)
	generic, found, err := provider.Sessions().GetBySessionID(t.Context(), intent.SessionID)
	if err != nil || !found {
		t.Fatalf("GetBySessionID() = %+v, %t, %v", generic, found, err)
	}
	generic.UserID = "metadata-writer"
	generic.ArchiveID = genericRuntimeMustNotWin
	generic.RuntimeSnapshotID = genericRuntimeMustNotWin
	generic.RuntimeGeneration = 99
	generic.EffectiveModel = genericRuntimeMustNotWin
	generic.RuntimeOutcome = string(RuntimeOperationOutcomeFailed)

	start := make(chan struct{})
	errs := make(chan error, 2)
	go func() {
		<-start
		errs <- provider.Sessions().Upsert(t.Context(), generic)
	}()
	go func() {
		<-start
		_, finalizeErr := provider.RuntimeOperations().Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: intent.OperationID, InputHash: intent.InputHash,
			Outcome: RuntimeOperationOutcomeContextPreserved, At: applying.UpdatedAt.Add(runtimeOperationTestStep),
		})
		errs <- finalizeErr
	}()
	close(start)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("concurrent writer error = %v", err)
		}
	}
	committed, found, err := provider.Sessions().GetBySessionID(t.Context(), intent.SessionID)
	if err != nil || !found {
		t.Fatalf("GetBySessionID() = %+v, %t, %v", committed, found, err)
	}
	if committed.UserID != generic.UserID || committed.ArchiveID != intent.TargetArchiveID ||
		committed.RuntimeGeneration != intent.ExpectedGeneration+1 || committed.RuntimeSnapshotID != intent.TargetSnapshotID ||
		committed.EffectiveModel != intent.TargetModel || committed.RuntimeOutcome != string(RuntimeOperationOutcomeContextPreserved) {
		t.Fatalf("committed session = %+v", committed)
	}
}

func checkRuntimeOperationStore(t *testing.T, provider Provider) {
	t.Helper()
	store := provider.RuntimeOperations()
	baseTime := time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC)
	putRuntimeSession(t, provider, "session-publish", "archive-one", 4)
	intent := testRuntimeIntent("operation-publish", "session-publish", "a", baseTime)

	const contenders = 8
	var wg sync.WaitGroup
	errorsSeen := make(chan error, contenders)
	for range contenders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			got, err := store.Begin(t.Context(), intent)
			if err == nil && (got.OperationID != intent.OperationID || got.State != RuntimeOperationPrepared) {
				err = errors.New("Begin returned wrong durable intent")
			}
			errorsSeen <- err
		}()
	}
	wg.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("concurrent Begin() error = %v", err)
		}
	}

	changedHash := intent
	changedHash.InputHash = strings.Repeat("b", 64)
	if _, err := store.Begin(t.Context(), changedHash); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("Begin(changed hash) error = %v, want conflict", err)
	}
	changedTarget := intent
	changedTarget.TargetModel = "different-model"
	if _, err := store.Begin(t.Context(), changedTarget); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("Begin(changed target) error = %v, want conflict", err)
	}
	second := testRuntimeIntent("operation-other", intent.SessionID, "c", baseTime)
	if _, err := store.Begin(t.Context(), second); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("Begin(second unresolved) error = %v, want conflict", err)
	}
	got, found, err := store.GetUnresolvedBySession(t.Context(), intent.SessionID)
	if err != nil || !found || got.OperationID != intent.OperationID {
		t.Fatalf("GetUnresolvedBySession() = %+v, %t, %v", got, found, err)
	}
	if err := store.MarkApplying(t.Context(), intent.OperationID, strings.Repeat("d", 64)); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("MarkApplying(wrong hash) error = %v, want conflict", err)
	}
	if err := store.MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatalf("MarkApplying() error = %v", err)
	}
	if err := store.MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatalf("MarkApplying(replay) error = %v", err)
	}
	applying := getRuntimeOperation(t, store, intent.OperationID)
	if applying.State != RuntimeOperationApplying {
		t.Fatalf("Get(applying) = %+v", applying)
	}

	final := RuntimeOperationFinalization{
		OperationID: intent.OperationID, InputHash: intent.InputHash,
		Outcome: RuntimeOperationOutcomeContextPreserved, At: applying.UpdatedAt.Add(runtimeOperationTestStep),
	}
	terminal, err := store.Finalize(t.Context(), final)
	if err != nil || terminal.State != RuntimeOperationTerminal || terminal.Outcome != string(final.Outcome) || terminal.TerminalAt == nil {
		t.Fatalf("Finalize() = %+v, %v", terminal, err)
	}
	replayed, err := store.Finalize(t.Context(), final)
	if err != nil || replayed.State != RuntimeOperationTerminal {
		t.Fatalf("Finalize(replay) = %+v, %v", replayed, err)
	}
	if err := store.MarkApplying(t.Context(), intent.OperationID, intent.InputHash); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("MarkApplying(terminal) error = %v, want conflict", err)
	}
	conflictingFinal := final
	conflictingFinal.Outcome = RuntimeOperationOutcomeAborted
	if _, err := store.Finalize(t.Context(), conflictingFinal); !errors.Is(err, ErrRuntimeOperationConflict) {
		t.Fatalf("Finalize(conflicting replay) error = %v, want conflict", err)
	}
	committed, found, err := provider.Sessions().GetBySessionID(t.Context(), intent.SessionID)
	if err != nil || !found {
		t.Fatalf("GetBySessionID() = %+v, %t, %v", committed, found, err)
	}
	if committed.ArchiveID != intent.TargetArchiveID || committed.RuntimeGeneration != intent.ExpectedGeneration+1 ||
		committed.RuntimeSnapshotID != intent.TargetSnapshotID || committed.ProviderBindingJSON != testRuntimeBinding ||
		committed.EffectiveModel != intent.TargetModel || committed.RuntimeOutcome != string(final.Outcome) {
		t.Fatalf("canonical session after Finalize = %+v", committed)
	}
	if unresolved, found, err := store.GetUnresolvedBySession(t.Context(), intent.SessionID); err != nil || found {
		t.Fatalf("GetUnresolvedBySession(terminal) = %+v, %t, %v", unresolved, found, err)
	}

	t.Run("concurrent finalization commits one generation", func(t *testing.T) {
		const finalizers = 16
		putRuntimeSession(t, provider, "session-finalize-cas", "archive-one", 4)
		candidate := testRuntimeIntent("operation-finalize-cas", "session-finalize-cas", "6", baseTime)
		if _, err := store.Begin(t.Context(), candidate); err != nil {
			t.Fatal(err)
		}
		applying := markRuntimeOperationApplying(t, store, candidate)
		finalization := RuntimeOperationFinalization{
			OperationID: candidate.OperationID, InputHash: candidate.InputHash,
			Outcome: RuntimeOperationOutcomeContextPreserved, ReasonCode: RuntimeOperationReasonChangedLive,
			At: applying.UpdatedAt.Add(runtimeOperationTestStep),
		}
		start := make(chan struct{})
		results := make(chan RuntimeOperationRecord, finalizers)
		errs := make(chan error, finalizers)
		var group sync.WaitGroup
		for range finalizers {
			group.Add(1)
			go func() {
				defer group.Done()
				<-start
				result, err := store.Finalize(t.Context(), finalization)
				results <- result
				errs <- err
			}()
		}
		close(start)
		group.Wait()
		close(results)
		close(errs)
		for err := range errs {
			if err != nil {
				t.Errorf("concurrent Finalize() error = %v", err)
			}
		}
		for result := range results {
			if result.State != RuntimeOperationTerminal || result.Outcome != string(finalization.Outcome) ||
				result.ReasonCode != string(finalization.ReasonCode) {
				t.Errorf("concurrent Finalize() = %+v", result)
			}
		}
		committed, found, err := provider.Sessions().GetBySessionID(t.Context(), candidate.SessionID)
		if err != nil || !found || committed.RuntimeGeneration != candidate.ExpectedGeneration+1 {
			t.Fatalf("session after concurrent Finalize() = %+v, %t, %v", committed, found, err)
		}
	})

	t.Run("non-publishing outcomes and reasons round trip", func(t *testing.T) {
		cases := []struct {
			outcome RuntimeOperationOutcome
			reason  RuntimeOperationReason
		}{
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonUnsupported},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonInvalid},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonIncompatible},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonRejected},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonBusy},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonClosed},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonUnavailable},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonPreDispatch},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonCanceled},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonDeadlineExceeded},
			{RuntimeOperationOutcomeAborted, RuntimeOperationReasonRecoveryAborted},
			{RuntimeOperationOutcomeConflict, RuntimeOperationReasonGuardConflict},
			{RuntimeOperationOutcomeFailed, RuntimeOperationReasonFailed},
			{RuntimeOperationOutcomeCleanupIncomplete, RuntimeOperationReasonCleanupIncomplete},
		}
		for index, test := range cases {
			operationID := "operation-model-noop-" + string(test.reason)
			sessionID := "session-model-noop-" + string(test.reason)
			hexDigit := "0123456789abcdef"[index : index+1]
			candidate := testRuntimeIntent(operationID, sessionID, hexDigit, baseTime.Add(time.Duration(index+1)*time.Minute))
			if _, err := store.Begin(t.Context(), candidate); err != nil {
				t.Fatalf("Begin(%s): %v", test.reason, err)
			}
			final := RuntimeOperationFinalization{
				OperationID: candidate.OperationID, InputHash: candidate.InputHash,
				Outcome: test.outcome, ReasonCode: test.reason,
				At: candidate.UpdatedAt.Add(time.Second),
			}
			terminal, err := store.Finalize(t.Context(), final)
			if err != nil || terminal.Outcome != string(test.outcome) || terminal.ReasonCode != string(test.reason) {
				t.Fatalf("Finalize(%s) = %+v, %v", test.reason, terminal, err)
			}
			replayed, err := store.Finalize(t.Context(), final)
			if err != nil || !reflect.DeepEqual(replayed, terminal) {
				t.Fatalf("Finalize replay(%s) = %+v, %v", test.reason, replayed, err)
			}
		}
	})

	t.Run("publishing outcomes and reasons round trip", func(t *testing.T) {
		cases := []struct {
			outcome RuntimeOperationOutcome
			reason  RuntimeOperationReason
			digit   string
		}{
			{RuntimeOperationOutcomeContextPreserved, RuntimeOperationReasonChangedLive, "1"},
			{RuntimeOperationOutcomeContextReset, RuntimeOperationReasonChangedReplacement, "2"},
		}
		for index, test := range cases {
			sessionID := "session-publishing-" + string(test.reason)
			operationID := "operation-publishing-" + string(test.reason)
			putRuntimeSession(t, provider, sessionID, "archive-one", 4)
			candidate := testRuntimeIntent(operationID, sessionID, test.digit, baseTime.Add(time.Duration(index+1)*time.Hour))
			if _, err := store.Begin(t.Context(), candidate); err != nil {
				t.Fatalf("Begin(%s): %v", test.reason, err)
			}
			applying := markRuntimeOperationApplying(t, store, candidate)
			final := RuntimeOperationFinalization{
				OperationID: candidate.OperationID, InputHash: candidate.InputHash,
				Outcome: test.outcome, ReasonCode: test.reason,
				At: applying.UpdatedAt.Add(runtimeOperationTestStep),
			}
			terminal, err := store.Finalize(t.Context(), final)
			if err != nil || terminal.Outcome != string(test.outcome) || terminal.ReasonCode != string(test.reason) {
				t.Fatalf("Finalize(%s) = %+v, %v", test.reason, terminal, err)
			}
			committed, found, err := provider.Sessions().GetBySessionID(t.Context(), sessionID)
			if err != nil || !found || committed.RuntimeGeneration != 5 || committed.RuntimeOutcome != string(test.outcome) {
				t.Fatalf("published session(%s) = %+v, %t, %v", test.reason, committed, found, err)
			}
		}
	})

	t.Run("public binding canonicalization is replay safe", func(t *testing.T) {
		const sessionID = "session-canonical-binding"
		putRuntimeSession(t, provider, sessionID, "archive-one", 4)
		candidate := testRuntimeIntent("operation-canonical-binding", sessionID, "0", baseTime)
		candidate.TargetProviderBindingJSON = testRuntimeBindingNoncanonical
		prepared, err := store.Begin(t.Context(), candidate)
		if err != nil {
			t.Fatal(err)
		}
		if prepared.TargetProviderBindingJSON != testRuntimeBinding {
			t.Fatalf("Begin() binding = %q, want canonical %q", prepared.TargetProviderBindingJSON, testRuntimeBinding)
		}
		replay := candidate
		replay.TargetProviderBindingJSON = "\n\t" + testRuntimeBinding + "\n"
		replayed, err := store.Begin(t.Context(), replay)
		if err != nil || !reflect.DeepEqual(replayed, prepared) {
			t.Fatalf("Begin(semantic replay) = %+v, %v, want %+v", replayed, err, prepared)
		}
		persisted := getRuntimeOperation(t, store, candidate.OperationID)
		if !reflect.DeepEqual(persisted, prepared) {
			t.Fatalf("Get() = %+v, want %+v", persisted, prepared)
		}
		applying := markRuntimeOperationApplying(t, store, candidate)
		terminal, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: candidate.OperationID, InputHash: candidate.InputHash,
			Outcome: RuntimeOperationOutcomeContextPreserved, ReasonCode: RuntimeOperationReasonChangedReplacement,
			At: applying.UpdatedAt.Add(runtimeOperationTestStep),
		})
		if err != nil || terminal.TargetProviderBindingJSON != testRuntimeBinding {
			t.Fatalf("Finalize() = %+v, %v", terminal, err)
		}
		committed, found, err := provider.Sessions().GetBySessionID(t.Context(), sessionID)
		if err != nil || !found || committed.ProviderBindingJSON != testRuntimeBinding {
			t.Fatalf("published session binding = %q, found=%t, error=%v", committed.ProviderBindingJSON, found, err)
		}
	})

	t.Run("guard conflict rolls back", func(t *testing.T) {
		putRuntimeSession(t, provider, "session-conflict", "archive-current", 2)
		candidate := testRuntimeIntent("operation-conflict", "session-conflict", "e", baseTime)
		candidate.ExpectedArchiveID = "archive-stale"
		candidate.ExpectedGeneration = 1
		if _, err := store.Begin(t.Context(), candidate); err != nil {
			t.Fatal(err)
		}
		applying := markRuntimeOperationApplying(t, store, candidate)
		_, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: candidate.OperationID, InputHash: candidate.InputHash,
			Outcome: RuntimeOperationOutcomeContextReset, At: applying.UpdatedAt.Add(runtimeOperationTestStep),
		})
		if !errors.Is(err, ErrRuntimeOperationConflict) {
			t.Fatalf("Finalize(stale guard) error = %v, want conflict", err)
		}
		persisted, found, err := store.Get(t.Context(), candidate.OperationID)
		if err != nil || !found || persisted.State != RuntimeOperationApplying {
			t.Fatalf("operation after rolled-back Finalize = %+v, %t, %v", persisted, found, err)
		}
		unchanged, found, err := provider.Sessions().GetBySessionID(t.Context(), candidate.SessionID)
		if err != nil || !found || unchanged.ArchiveID != "archive-current" || unchanged.RuntimeGeneration != 2 {
			t.Fatalf("session after rolled-back Finalize = %+v, %t, %v", unchanged, found, err)
		}
		terminal, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: candidate.OperationID, InputHash: candidate.InputHash,
			Outcome: RuntimeOperationOutcomeConflict, ReasonCode: RuntimeOperationReasonGuardConflict,
			At: persisted.UpdatedAt.Add(runtimeOperationTestStep),
		})
		if err != nil || terminal.State != RuntimeOperationTerminal || terminal.Outcome != string(RuntimeOperationOutcomeConflict) {
			t.Fatalf("Finalize(authoritative stale conflict) = %+v, %v", terminal, err)
		}
		unchanged, found, err = provider.Sessions().GetBySessionID(t.Context(), candidate.SessionID)
		if err != nil || !found || unchanged.ArchiveID != "archive-current" || unchanged.RuntimeGeneration != 2 || unchanged.EffectiveModel != "model-old" {
			t.Fatalf("session after nonpublishing conflict = %+v, %t, %v", unchanged, found, err)
		}
	})

	t.Run("prepared abort survives missing session", func(t *testing.T) {
		abort := testRuntimeIntent("operation-missing-session", "session-missing", "3", baseTime)
		if _, err := store.Begin(t.Context(), abort); err != nil {
			t.Fatal(err)
		}
		terminal, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: abort.OperationID, InputHash: abort.InputHash,
			Outcome: RuntimeOperationOutcomeAborted, ReasonCode: RuntimeOperationReasonRecoveryAborted,
			At: baseTime.Add(time.Hour),
		})
		if err != nil || terminal.State != RuntimeOperationTerminal {
			t.Fatalf("Finalize(missing-session abort) = %+v, %v", terminal, err)
		}
		failed := testRuntimeIntent("operation-missing-session-failed", "session-missing", "7", baseTime.Add(2*time.Hour))
		if _, err := store.Begin(t.Context(), failed); err != nil {
			t.Fatal(err)
		}
		if err := store.MarkApplying(t.Context(), failed.OperationID, failed.InputHash); err != nil {
			t.Fatal(err)
		}
		applying, found, err := store.Get(t.Context(), failed.OperationID)
		if err != nil || !found || applying.State != RuntimeOperationApplying {
			t.Fatalf("Get(applying missing-session failure) = %+v, %t, %v", applying, found, err)
		}
		terminal, err = store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: failed.OperationID, InputHash: failed.InputHash,
			Outcome: RuntimeOperationOutcomeFailed, ReasonCode: RuntimeOperationReasonFailed,
			At: applying.UpdatedAt.Add(time.Hour),
		})
		if err != nil || terminal.State != RuntimeOperationTerminal || terminal.Outcome != string(RuntimeOperationOutcomeFailed) {
			t.Fatalf("Finalize(missing-session failure) = %+v, %v", terminal, err)
		}
	})

	t.Run("prepared abort and terminal-only bounded prune", func(t *testing.T) {
		putRuntimeSession(t, provider, "session-abort", "archive-abort", 0)
		abort := testRuntimeIntent("operation-abort", "session-abort", "f", baseTime.Add(-2*time.Hour))
		abort.ExpectedArchiveID = "archive-abort"
		abort.ExpectedGeneration = 0
		if _, err := store.Begin(t.Context(), abort); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: abort.OperationID, InputHash: abort.InputHash,
			Outcome: RuntimeOperationOutcomeAborted, ReasonCode: RuntimeOperationReasonRecoveryAborted,
			At: baseTime.Add(-time.Hour),
		}); err != nil {
			t.Fatal(err)
		}
		putRuntimeSession(t, provider, "session-pending", "archive-pending", 0)
		pending := testRuntimeIntent("operation-pending", "session-pending", "1", baseTime.Add(-3*time.Hour))
		pending.ExpectedArchiveID = "archive-pending"
		pending.ExpectedGeneration = 0
		if _, err := store.Begin(t.Context(), pending); err != nil {
			t.Fatal(err)
		}
		if count, err := store.Prune(t.Context(), baseTime, 1); err != nil || count != 1 {
			t.Fatalf("Prune() = %d, %v, want 1", count, err)
		}
		if _, found, err := store.Get(t.Context(), pending.OperationID); err != nil || !found {
			t.Fatalf("prepared operation after Prune found=%t error=%v", found, err)
		}
		if err := provider.Sessions().DeleteBySessionID(t.Context(), pending.SessionID); err != nil {
			t.Fatal(err)
		}
		if _, found, err := store.Get(t.Context(), pending.OperationID); err != nil || !found {
			t.Fatalf("prepared operation after session deletion found=%t error=%v", found, err)
		}
		if _, err := store.Prune(t.Context(), baseTime, maxRuntimeOperationPrune+1); !errors.Is(err, ErrRuntimeOperationInvalid) {
			t.Fatalf("Prune(unbounded) error = %v, want invalid", err)
		}
	})

	t.Run("semantic and protected input validation", func(t *testing.T) {
		invalid := testRuntimeIntent("operation-invalid", "session-invalid", "2", baseTime)
		invalid.Kind = "arbitrary-command"
		if _, err := store.Begin(t.Context(), invalid); !errors.Is(err, ErrRuntimeOperationInvalid) {
			t.Fatalf("Begin(unknown kind) error = %v, want invalid", err)
		}
		invalid.Kind = string(RuntimeOperationKindModel)
		invalid.TargetProviderBindingJSON = strings.Replace(testRuntimeBinding, "}", `,"api_key":"must-not-land"}`, 1)
		if _, err := store.Begin(t.Context(), invalid); !errors.Is(err, ErrRuntimeOperationInvalid) || strings.Contains(err.Error(), "must-not-land") {
			t.Fatalf("Begin(protected binding) error = %v", err)
		}
		if _, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
			OperationID: "operation-invalid", InputHash: strings.Repeat("2", 64),
			Outcome: "raw-provider-error", At: baseTime,
		}); !errors.Is(err, ErrRuntimeOperationInvalid) {
			t.Fatalf("Finalize(unknown outcome) error = %v, want invalid", err)
		}
	})

	t.Run("same-second terminal cutoff", func(t *testing.T) {
		second := time.Date(2020, 1, 2, 3, 4, 5, 0, time.UTC)
		for _, item := range []struct {
			id    string
			digit string
			at    time.Time
		}{
			{id: "operation-cutoff-before", digit: "4", at: second.Add(100 * time.Microsecond)},
			{id: "operation-cutoff-after", digit: "5", at: second.Add(200 * time.Microsecond)},
		} {
			intent := testRuntimeIntent(item.id, "session-"+item.id, item.digit, second.Add(50*time.Microsecond))
			if _, err := store.Begin(t.Context(), intent); err != nil {
				t.Fatal(err)
			}
			if _, err := store.Finalize(t.Context(), RuntimeOperationFinalization{
				OperationID: intent.OperationID, InputHash: intent.InputHash,
				Outcome: RuntimeOperationOutcomeAborted, ReasonCode: RuntimeOperationReasonRecoveryAborted, At: item.at,
			}); err != nil {
				t.Fatal(err)
			}
		}
		db, bind, _ := runtimeOperationTestSQL(provider)
		insertLegacy := bind(`INSERT INTO balda_runtime_operations(
			operation_id,session_id,input_hash,kind,state,expected_archive_id,expected_generation,
			outcome,reason_code,created_at,updated_at,terminal_at)
			VALUES(?,?,?,'model','terminal','',0,'aborted','recovery_aborted',?,?,?)`)
		for _, legacy := range []struct {
			id        string
			digit     string
			timestamp string
		}{
			{id: "operation-cutoff-legacy-whole", digit: "6", timestamp: "2020-01-02T03:04:05Z"},
			{id: "operation-cutoff-legacy-before", digit: "7", timestamp: "2020-01-02T03:04:05.000100000Z"},
			{id: "operation-cutoff-legacy-after", digit: "8", timestamp: "2020-01-02T03:04:05.000200000Z"},
		} {
			if _, err := db.ExecContext(t.Context(), insertLegacy,
				legacy.id, "session-"+legacy.id, strings.Repeat(legacy.digit, 64),
				legacy.timestamp, legacy.timestamp, legacy.timestamp); err != nil {
				t.Fatal(err)
			}
		}
		cutoff := second.Add(150 * time.Microsecond)
		if count, err := store.Prune(t.Context(), cutoff, 10); err != nil || count != 3 {
			t.Fatalf("Prune(same-second cutoff) = %d, %v, want 3", count, err)
		}
		if _, found, err := store.Get(t.Context(), "operation-cutoff-before"); err != nil || found {
			t.Fatalf("before-cutoff operation found=%t error=%v", found, err)
		}
		for _, operationID := range []string{"operation-cutoff-legacy-whole", "operation-cutoff-legacy-before"} {
			if _, found, err := store.Get(t.Context(), operationID); err != nil || found {
				t.Fatalf("legacy before-cutoff operation %q found=%t error=%v", operationID, found, err)
			}
		}
		if _, found, err := store.Get(t.Context(), "operation-cutoff-after"); err != nil || !found {
			t.Fatalf("after-cutoff operation found=%t error=%v", found, err)
		}
		if _, found, err := store.Get(t.Context(), "operation-cutoff-legacy-after"); err != nil || !found {
			t.Fatalf("legacy after-cutoff operation found=%t error=%v", found, err)
		}
	})

	t.Run("receipts canonicalize sub-microsecond timestamps", func(t *testing.T) {
		zone := time.FixedZone("test-offset", 3*60*60)
		inputAt := time.Date(2026, 10, 27, 15, 16, 17, 123456789, zone)
		intent := testRuntimeIntent("operation-receipt-time", "session-receipt-time", "9", inputAt)
		firstBegin, err := store.Begin(t.Context(), intent)
		if err != nil {
			t.Fatal(err)
		}
		replayedBegin, err := store.Begin(t.Context(), intent)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(firstBegin, replayedBegin) {
			t.Fatalf("Begin receipt differs from replay: first=%+v replay=%+v", firstBegin, replayedBegin)
		}
		wantBeginAt := inputAt.UTC().Truncate(time.Microsecond)
		if firstBegin.CreatedAt != wantBeginAt || firstBegin.UpdatedAt != wantBeginAt {
			t.Fatalf("Begin timestamps = %v/%v, want %v", firstBegin.CreatedAt, firstBegin.UpdatedAt, wantBeginAt)
		}

		finalAt := inputAt.Add(time.Second + 987*time.Nanosecond)
		finalization := RuntimeOperationFinalization{
			OperationID: intent.OperationID, InputHash: intent.InputHash,
			Outcome: RuntimeOperationOutcomeAborted, ReasonCode: RuntimeOperationReasonRecoveryAborted, At: finalAt,
		}
		firstFinal, err := store.Finalize(t.Context(), finalization)
		if err != nil {
			t.Fatal(err)
		}
		replayedFinal, err := store.Finalize(t.Context(), finalization)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(firstFinal, replayedFinal) {
			t.Fatalf("Finalize receipt differs from replay: first=%+v replay=%+v", firstFinal, replayedFinal)
		}
		wantFinalAt := finalAt.UTC().Truncate(time.Microsecond)
		if firstFinal.UpdatedAt != wantFinalAt || firstFinal.TerminalAt == nil || *firstFinal.TerminalAt != wantFinalAt {
			t.Fatalf("Finalize timestamps = %v/%v, want %v", firstFinal.UpdatedAt, firstFinal.TerminalAt, wantFinalAt)
		}
	})
}

func runtimeOperationTestSQL(provider Provider) (*sql.DB, func(string) string, bool) {
	switch provider := provider.(type) {
	case *sqliteProvider:
		return provider.db, func(query string) string { return query }, false
	case *postgresProvider:
		return provider.db, postgresBind, true
	default:
		panic("unsupported runtime operation test provider")
	}
}

func putRuntimeSession(t *testing.T, provider Provider, sessionID, archiveID string, generation uint64) {
	t.Helper()
	err := provider.Sessions().Upsert(t.Context(), SessionRecord{
		SessionID: sessionID, UserID: "user", ChannelType: ChannelTypeTelegram,
		AddressKey: sessionID + ":0", AddressJSON: `{}`, AgentName: "agent",
		RuntimeSnapshotID: "snapshot-old", ProviderBindingJSON: testRuntimeBinding,
		ArchiveID: archiveID, RuntimeGeneration: generation, EffectiveModel: "model-old",
		RuntimeOutcome: "context_preserved", Status: SessionStatusActive,
	})
	if err != nil {
		t.Fatalf("put runtime session: %v", err)
	}
}

func testRuntimeIntent(operationID, sessionID, hashDigit string, at time.Time) RuntimeOperationRecord {
	return RuntimeOperationRecord{
		OperationID: operationID, SessionID: sessionID, InputHash: strings.Repeat(hashDigit, 64),
		Kind: string(RuntimeOperationKindModel), State: RuntimeOperationPrepared,
		ExpectedArchiveID: "archive-one", ExpectedGeneration: 4,
		TargetArchiveID: "archive-two", TargetSnapshotID: "snapshot-next",
		TargetProviderBindingJSON: testRuntimeBinding, TargetModel: "model-next",
		CreatedAt: at, UpdatedAt: at,
	}
}

func markRuntimeOperationApplying(t *testing.T, store RuntimeOperationStore, intent RuntimeOperationRecord) RuntimeOperationRecord {
	t.Helper()
	if err := store.MarkApplying(t.Context(), intent.OperationID, intent.InputHash); err != nil {
		t.Fatalf("MarkApplying(%q): %v", intent.OperationID, err)
	}
	operation := getRuntimeOperation(t, store, intent.OperationID)
	if operation.State != RuntimeOperationApplying {
		t.Fatalf("Get(%q) state = %q, want applying", intent.OperationID, operation.State)
	}
	return operation
}

func getRuntimeOperation(t *testing.T, store RuntimeOperationStore, operationID string) RuntimeOperationRecord {
	t.Helper()
	operation, found, err := store.Get(t.Context(), operationID)
	if err != nil || !found {
		t.Fatalf("Get(%q) = %+v, %t, %v", operationID, operation, found, err)
	}
	return operation
}
