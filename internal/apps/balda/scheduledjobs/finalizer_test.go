package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

type recordingRunCloser struct{ calls []string }

func (c *recordingRunCloser) CloseRunSession(_ context.Context, sessionID, userID string) error {
	c.calls = append(c.calls, sessionID+":"+userID)
	return nil
}

type flakyRunCloser struct {
	recordingRunCloser
	failOnce bool
}

type blockedRunCloser struct {
	recordingRunCloser
	blockedSession string
}

func (c *blockedRunCloser) CloseRunSession(ctx context.Context, sessionID, userID string) error {
	if sessionID == c.blockedSession {
		return errors.New("workspace cleanup denied")
	}
	return c.recordingRunCloser.CloseRunSession(ctx, sessionID, userID)
}

func (c *flakyRunCloser) CloseRunSession(ctx context.Context, sessionID, userID string) error {
	if c.failOnce {
		c.failOnce = false
		return errors.New("runtime deletion interrupted")
	}
	return c.recordingRunCloser.CloseRunSession(ctx, sessionID, userID)
}

func TestRunFinalizerWaitsForSentReportAndRunDetailUsesItsPayload(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	const jobID = "scheduled-test-final"
	const runID = "run-final"
	input := "private instruction"
	snapshot, err := json.Marshal(state.ScheduledJobRecord{JobID: "daily", Content: input, ReportToEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
		RunID: runID, ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
		TriggerKey: "manual:test-final", DefinitionVersion: 1, RequestedAt: now,
		DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(snapshot),
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := turncmd.ScheduledExecutionSessionID(jobID)
	if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: jobID,
		SessionID: sessionID, Objective: input, Status: state.JobStatusCompleted,
		CreatedBy: "owner", CompletedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Jobs().RecordScheduledOutput(ctx, jobID, "the exact sent report"); err != nil {
		t.Fatal(err)
	}
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("closed before report: calls=%v err=%v", closer.calls, err)
	}
	if _, _, err := provider.Jobs().ReserveDelivery(ctx, state.DeliveryRecord{
		ID: "delivery-final", DeliveryKey: jobID + ":delivery:final", JobID: jobID,
		SessionID: sessionID, Channel: "telegram", AddressKey: "9001:0", Kind: "delivery",
		Payload: `{"text":"the exact sent report"}`, PayloadHash: "final-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("closed before delivery settled: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.Jobs().MarkDeliverySent(ctx, jobID+":delivery:final", deliverycmd.Result{ProviderMessageID: "telegram-message"}); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 ||
		closer.calls[0] != sessionID+":owner" {
		t.Fatalf("close after sent: calls=%v err=%v", closer.calls, err)
	}
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 {
		t.Fatalf("close repeated: calls=%v err=%v", closer.calls, err)
	}
	manager := NewManagement(nil, &recordingScheduleManagementStore{}, provider.ScheduleRuns(),
		provider.Jobs(), provider.Jobs())
	manager.jobs = provider.ScheduledJobs()
	if err := provider.ScheduledJobs().Upsert(ctx, state.ScheduledJobRecord{JobID: "daily",
		Source: state.ScheduledJobSourceManaged, Enabled: true, DefinitionVersion: 1,
		SessionID: "tg-9001-0", ChannelType: state.ChannelTypeTelegram, AddressKey: "9001:0", AddressJSON: `{}`,
		Content: input, ScheduleSpec: "0 9 * * *", Status: state.ScheduledJobStatusActive,
		NextRunAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	detail, err := manager.RunDetail(ctx, "daily", runID, schedulecmd.Authority{})
	if err != nil || detail.Input != input || detail.Output != "the exact sent report" || detail.Run.State != runStateSucceeded {
		t.Fatalf("run detail = %+v, err=%v", detail, err)
	}
}

func TestRunFinalizerSettlesSilentSuccessWithoutReportDelivery(t *testing.T) {
	for _, keepSession := range []bool{false, true} {
		name := "ephemeral"
		if keepSession {
			name = "retained"
		}
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			now := time.Now().UTC()
			const scheduleID = "daily"
			jobID := "scheduled-silent-" + name
			runID := "run-silent-" + name
			snapshot := state.ScheduledJobRecord{
				JobID: scheduleID, Content: "input", ReportToEnabled: true, KeepSession: keepSession,
			}
			if keepSession {
				snapshot.Source = state.ScheduledJobSourceManaged
				snapshot.Enabled = true
				snapshot.DefinitionVersion = 1
				snapshot.ScheduleSpec = "0 9 * * *"
				snapshot.Status = state.ScheduledJobStatusActive
				snapshot.NextRunAt = now.Add(time.Hour)
				if err := provider.ScheduledJobs().Upsert(ctx, snapshot); err != nil {
					t.Fatal(err)
				}
			}
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
				RunID: runID, ScheduleID: scheduleID, Trigger: state.ScheduleRunTriggerCron,
				TriggerKey: scheduleID + "@slot", DefinitionVersion: 1, RequestedAt: now,
				DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(payload),
			}); err != nil {
				t.Fatal(err)
			}
			sessionID := turncmd.ScheduledExecutionSessionID(jobID)
			if keepSession {
				sessionID = turncmd.PersistentScheduledExecutionSessionID(scheduleID)
			}
			if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{
				ID: jobID, SessionID: sessionID, Objective: "input", Status: state.JobStatusCompleted,
				CreatedBy: "owner", CompletedAt: now,
			}); err != nil {
				t.Fatal(err)
			}

			closer := &recordingRunCloser{}
			finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
			if err := finalizer.Settle(ctx, 10); err != nil {
				t.Fatal(err)
			}
			wantCloseCalls := 1
			if keepSession {
				wantCloseCalls = 0
			}
			if len(closer.calls) != wantCloseCalls {
				t.Fatalf("close calls = %v, want %d", closer.calls, wantCloseCalls)
			}
			if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != wantCloseCalls {
				t.Fatalf("repeated settle: calls=%v err=%v", closer.calls, err)
			}
			unclosed, err := provider.ScheduleRuns().ListUnclosedDispatched(ctx, time.Time{}, "", 10)
			if err != nil || len(unclosed) != 0 {
				t.Fatalf("unclosed silent runs = %v, err=%v", unclosed, err)
			}
		})
	}
}

func TestRunFinalizerClosesNoReportRunAfterExecution(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	const jobID = "scheduled-no-report"
	const runID = "run-no-report"
	now := time.Now().UTC()
	snapshot, err := json.Marshal(state.ScheduledJobRecord{JobID: "daily", Content: "input"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
		RunID: runID, ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
		TriggerKey: "manual:no-report", DefinitionVersion: 1, RequestedAt: now,
		DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(snapshot),
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := turncmd.ScheduledExecutionSessionID(jobID)
	if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: jobID,
		SessionID: sessionID, Objective: "input", Status: state.JobStatusCompleted, CreatedBy: "schedule-user",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Jobs().RecordScheduledOutput(ctx, jobID, "answer"); err != nil {
		t.Fatal(err)
	}
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 ||
		closer.calls[0] != sessionID+":schedule-user" {
		t.Fatalf("no-report close = %v, calls=%v", err, closer.calls)
	}
	if err := provider.ScheduledJobs().Upsert(ctx, state.ScheduledJobRecord{
		JobID: "daily", Source: state.ScheduledJobSourceManaged, Enabled: true,
		DefinitionVersion: 1, Content: "input", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManagement(provider.ScheduledJobs(), &recordingScheduleManagementStore{},
		provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	detail, err := manager.RunDetail(ctx, "daily", runID, schedulecmd.Authority{})
	if err != nil || detail.Input != "input" || detail.Output != "answer" || detail.Run.State != runStateSucceeded {
		t.Fatalf("no-report run detail = %+v, err=%v", detail, err)
	}
}

func TestRunFinalizerRetainsOrCleansStableScheduleSession(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		currentKeep bool
		activeNext  bool
		wantClose   int
	}{
		{name: "retained", currentKeep: true},
		{name: "disabled policy", wantClose: 1},
		{name: "disabled policy waits for active run", activeNext: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			ctx := t.Context()
			provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = provider.Close() })
			now := time.Now().UTC()
			definition := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceManaged,
				Enabled: true, KeepSession: testCase.currentKeep, DefinitionVersion: 2, Content: "input",
				ScheduleSpec: "0 9 * * *", Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(time.Hour)}
			if err := provider.ScheduledJobs().Upsert(ctx, definition); err != nil {
				t.Fatal(err)
			}
			snapshot := definition
			snapshot.KeepSession = true
			payload, err := json.Marshal(snapshot)
			if err != nil {
				t.Fatal(err)
			}
			const jobID = "scheduled-daily-retained"
			if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
				RunID: "run-retained", ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
				TriggerKey: "manual:retained", DefinitionVersion: 1, RequestedAt: now,
				DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(payload),
			}); err != nil {
				t.Fatal(err)
			}
			sessionID := turncmd.PersistentScheduledExecutionSessionID("daily")
			if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: jobID, SessionID: sessionID,
				Objective: "input", Status: state.JobStatusCompleted,
				CreatedBy: turncmd.ScheduledExecutionUserID(sessionID), CompletedAt: now}); err != nil {
				t.Fatal(err)
			}
			if testCase.activeNext {
				if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: "scheduled-daily-next", SessionID: sessionID,
					Objective: "next", Status: state.JobStatusRunning,
					CreatedBy: turncmd.ScheduledExecutionUserID(sessionID)}); err != nil {
					t.Fatal(err)
				}
			}
			closer := &recordingRunCloser{}
			finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
			if err := finalizer.Settle(ctx, 10); err != nil {
				t.Fatal(err)
			}
			if got := len(closer.calls); got != testCase.wantClose {
				t.Fatalf("close calls = %d, want %d", got, testCase.wantClose)
			}
			if testCase.activeNext {
				if err := provider.Jobs().UpdateJobStatus(ctx, "scheduled-daily-next", state.JobStatusCompleted, ""); err != nil {
					t.Fatal(err)
				}
				if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 {
					t.Fatalf("close after active run: calls=%v err=%v", closer.calls, err)
				}
			}
		})
	}
}

func TestRunFinalizerRetriesRetainedSessionCleanupAfterRestart(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Now().UTC()
	definition := state.ScheduledJobRecord{
		JobID: "daily", Source: state.ScheduledJobSourceManaged, Deleted: true,
		KeepSession: true, DefinitionVersion: 2, Content: "input",
		ScheduleSpec: "0 9 * * *", Status: state.ScheduledJobStatusActive,
		NextRunAt: now.Add(time.Hour),
	}
	if err := provider.ScheduledJobs().Upsert(ctx, definition); err != nil {
		t.Fatal(err)
	}
	payload, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	const jobID = "scheduled-daily-retained"
	if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
		RunID: "run-retained", ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
		TriggerKey: "manual:retained", DefinitionVersion: 1, RequestedAt: now,
		DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(payload),
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := turncmd.PersistentScheduledExecutionSessionID("daily")
	if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{
		ID: jobID, SessionID: sessionID, Objective: "input", Status: state.JobStatusCompleted,
		CreatedBy: turncmd.ScheduledExecutionUserID(sessionID), CompletedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	closer := &flakyRunCloser{failOnce: true}
	first := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := first.Settle(ctx, 10); err == nil {
		t.Fatal("first retained cleanup succeeded despite injected failure")
	}
	second := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := second.Settle(ctx, 10); err != nil {
		t.Fatalf("retry retained cleanup after restart: %v", err)
	}
	if len(closer.calls) != 1 || closer.calls[0] != sessionID+":"+turncmd.ScheduledExecutionUserID(sessionID) {
		t.Fatalf("retained cleanup calls = %v", closer.calls)
	}
}

func TestRunFinalizerClosesCanceledRunWithoutReportDelivery(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	const jobID = "scheduled-canceled-report"
	now := time.Now().UTC()
	snapshot, err := json.Marshal(state.ScheduledJobRecord{JobID: "daily", Content: "input", ReportToEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
		RunID: "run-canceled-report", ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
		TriggerKey: "manual:canceled-report", DefinitionVersion: 1, RequestedAt: now,
		DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(snapshot),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{
		ID: jobID, SessionID: turncmd.ScheduledExecutionSessionID(jobID),
		Objective: "input", Status: state.JobStatusCanceled, CreatedBy: "owner", CanceledAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 1); err != nil || len(closer.calls) != 1 {
		t.Fatalf("canceled cleanup: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.ScheduledJobs().Upsert(ctx, state.ScheduledJobRecord{
		JobID: "daily", Source: state.ScheduledJobSourceManaged, Enabled: true,
		DefinitionVersion: 1, Content: "input", ScheduleSpec: "0 9 * * *",
		Status: state.ScheduledJobStatusActive, NextRunAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	manager := NewManagement(provider.ScheduledJobs(), &recordingScheduleManagementStore{},
		provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	detail, err := manager.RunDetail(ctx, "daily", "run-canceled-report", schedulecmd.Authority{})
	if err != nil || detail.Run.State != runStateCanceled {
		t.Fatalf("canceled run detail = %+v, err=%v", detail, err)
	}
}

func TestRunFinalizerDoesNotStarveNoReportRunBehindPendingDelivery(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Now().UTC()
	for index, testCase := range []struct {
		jobID  string
		runID  string
		report bool
	}{
		{jobID: "scheduled-starvation-a", runID: "run-starvation-a", report: true},
		{jobID: "scheduled-starvation-b", runID: "run-starvation-b"},
	} {
		snapshot, err := json.Marshal(state.ScheduledJobRecord{
			JobID: "daily", Content: "input", ReportToEnabled: testCase.report,
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
			RunID: testCase.runID, ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
			TriggerKey: "manual:" + testCase.runID, DefinitionVersion: 1,
			RequestedAt:   now.Add(time.Duration(index) * time.Second),
			DispatchState: state.ScheduleRunDispatched, ExecutionJobID: testCase.jobID,
			PayloadJSON: string(snapshot),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{
			ID: testCase.jobID, SessionID: turncmd.ScheduledExecutionSessionID(testCase.jobID),
			Objective: "input", Status: state.JobStatusCompleted, CreatedBy: "owner",
		}); err != nil {
			t.Fatal(err)
		}
		if testCase.report {
			if err := provider.Jobs().RecordScheduledOutput(ctx, testCase.jobID, "pending report"); err != nil {
				t.Fatal(err)
			}
		}
	}
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if len(closer.calls) != 1 ||
		closer.calls[0] != turncmd.ScheduledExecutionSessionID("scheduled-starvation-b")+":owner" {
		t.Fatalf("closed sessions = %v, want no-report run", closer.calls)
	}
}

func TestRunFinalizerAdvancesPastFailedCleanup(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Now().UTC()
	for index, id := range []string{"blocked", "ready"} {
		jobID := "scheduled-cleanup-" + id
		snapshot, err := json.Marshal(state.ScheduledJobRecord{JobID: "daily", Content: "input"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
			RunID: "run-" + id, ScheduleID: "daily", Trigger: state.ScheduleRunTriggerManual,
			TriggerKey: "manual:" + id, DefinitionVersion: 1,
			RequestedAt:   now.Add(time.Duration(index) * time.Second),
			DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID,
			PayloadJSON: string(snapshot),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{
			ID: jobID, SessionID: turncmd.ScheduledExecutionSessionID(jobID),
			Objective: "input", Status: state.JobStatusCompleted, CreatedBy: "owner",
		}); err != nil {
			t.Fatal(err)
		}
	}
	closer := &blockedRunCloser{blockedSession: turncmd.ScheduledExecutionSessionID("scheduled-cleanup-blocked")}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 1); err == nil {
		t.Fatal("first cleanup should fail")
	}
	if err := finalizer.Settle(ctx, 1); err != nil {
		t.Fatalf("later cleanup blocked: %v", err)
	}
	if len(closer.calls) != 1 ||
		closer.calls[0] != turncmd.ScheduledExecutionSessionID("scheduled-cleanup-ready")+":owner" {
		t.Fatalf("closed sessions = %v, want later run", closer.calls)
	}
}

func TestRunFinalizerWaitsThroughAmbiguousDeliveryAndRetriesClose(t *testing.T) {
	ctx := t.Context()
	provider, err := state.NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	const jobID = "scheduled-ambiguous"
	const runID = "run-ambiguous"
	now := time.Now().UTC()
	snapshot, err := json.Marshal(state.ScheduledJobRecord{JobID: "daily", Content: "input", ReportToEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.ScheduleRuns().Create(ctx, state.ScheduleRunRecord{
		RunID: runID, ScheduleID: "daily", Trigger: state.ScheduleRunTriggerCron,
		TriggerKey: "daily@slot", DefinitionVersion: 1, RequestedAt: now,
		DispatchState: state.ScheduleRunDispatched, ExecutionJobID: jobID, PayloadJSON: string(snapshot),
	}); err != nil {
		t.Fatal(err)
	}
	sessionID := turncmd.ScheduledExecutionSessionID(jobID)
	if _, err := provider.Jobs().CreateJob(ctx, state.JobRecord{ID: jobID,
		SessionID: sessionID, Objective: "input", Status: state.JobStatusFailed,
		CreatedBy: "owner", CompletedAt: now}); err != nil {
		t.Fatal(err)
	}
	key := jobID + ":delivery:final"
	if _, _, err := provider.Jobs().ReserveDelivery(ctx, state.DeliveryRecord{
		ID: "delivery-ambiguous", DeliveryKey: key, JobID: jobID,
		SessionID: sessionID, Channel: "telegram", AddressKey: "9001:0", Kind: "delivery",
		Payload: `{"text":"bounded failure"}`, PayloadHash: "ambiguous-hash",
	}); err != nil {
		t.Fatal(err)
	}
	if err := provider.Jobs().MarkDeliverySending(ctx, key); err != nil {
		t.Fatal(err)
	}
	closer := &flakyRunCloser{failOnce: true}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.ScheduledJobs(), provider.Jobs(), provider.Jobs(), closer)
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 0 {
		t.Fatalf("ambiguous send closed run: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.Jobs().MarkDeliveryFailed(ctx, key, "permanent"); err != nil {
		t.Fatal(err)
	}
	if err := finalizer.Settle(ctx, 10); err == nil || len(closer.calls) != 0 {
		t.Fatalf("failed deletion marked closed: calls=%v err=%v", closer.calls, err)
	}
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 {
		t.Fatalf("close retry: calls=%v err=%v", closer.calls, err)
	}
	if err := finalizer.Settle(ctx, 10); err != nil || len(closer.calls) != 1 {
		t.Fatalf("replayed close: calls=%v err=%v", closer.calls, err)
	}
	if err := provider.ScheduledJobs().Upsert(ctx, state.ScheduledJobRecord{JobID: "daily",
		Source: state.ScheduledJobSourceManaged, Enabled: true, DefinitionVersion: 1,
		SessionID: "tg-9001-0", ChannelType: state.ChannelTypeTelegram, AddressKey: "9001:0", AddressJSON: `{}`,
		Content: "input", ScheduleSpec: "0 9 * * *", Status: state.ScheduledJobStatusActive,
		NextRunAt: now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	manager := NewManagement(provider.ScheduledJobs(), &recordingScheduleManagementStore{},
		provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	detail, err := manager.RunDetail(ctx, "daily", runID, schedulecmd.Authority{})
	if err != nil || detail.Input != "input" || detail.Output != "" ||
		detail.Run.State != runStateFailed || detail.Run.SafeFailureCode != "delivery_failed" {
		t.Fatalf("failed delivery detail = %+v, err=%v", detail, err)
	}
}
