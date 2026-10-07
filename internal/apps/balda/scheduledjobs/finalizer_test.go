package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
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
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
	if err := provider.Jobs().MarkDeliverySent(ctx, jobID+":delivery:final", "telegram-message"); err != nil {
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
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
	}
	closer := &recordingRunCloser{}
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
	finalizer := NewRunFinalizer(provider.ScheduleRuns(), provider.Jobs(), provider.Jobs(), closer)
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
