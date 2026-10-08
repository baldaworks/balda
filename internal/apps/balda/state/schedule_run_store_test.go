package state

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestScheduledJobStoreRejectsCrossSourceOverwrite(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	record := ScheduledJobRecord{JobID: "shared", Source: ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: "tg-1-0", ChannelType: ChannelTypeTelegram,
		AddressKey: "1:0", AddressJSON: `{}`, Content: "managed", ScheduleSpec: "0 9 * * *",
		Status: ScheduledJobStatusActive, NextRunAt: now}
	if err := provider.ScheduledJobs().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	record.Source = ScheduledJobSourceInternal
	record.Content = "overwrite"
	record.ScheduleSpec = "@once"
	if err := provider.ScheduledJobs().Upsert(t.Context(), record); !errors.Is(err, ErrScheduledJobSourceConflict) {
		t.Fatalf("Upsert(collision) = %v, want source conflict", err)
	}
	got, _, err := provider.ScheduledJobs().GetByID(t.Context(), "shared")
	if err != nil || got.Source != ScheduledJobSourceManaged || got.Content != "managed" {
		t.Fatalf("record after collision = %+v, %v", got, err)
	}
}

// This catches loss of run history when the schedule is removed or the database
// is reopened, and catches duplicate submission of the same due/manual key.
func TestScheduleRunStoreRetainsHistoryAfterScheduleDeletion(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	requestedAt := time.Date(2026, 10, 7, 9, 30, 0, 0, time.UTC)
	if err := provider.ScheduledJobs().Upsert(t.Context(), ScheduledJobRecord{
		JobID: "daily-review", Source: "managed", Enabled: true, DefinitionVersion: 1,
		SessionID: "tg-1-0", ChannelType: ChannelTypeTelegram, AddressKey: "1:0", AddressJSON: `{}`,
		Content: "review", ScheduleSpec: "0 9 * * *", Status: ScheduledJobStatusActive,
		NextRunAt: requestedAt.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	run := ScheduleRunRecord{
		RunID:             "run-1",
		ScheduleID:        "daily-review",
		Trigger:           ScheduleRunTriggerManual,
		TriggerKey:        "manual:request-1",
		DefinitionVersion: 1,
		RequestedAt:       requestedAt,
		DispatchState:     ScheduleRunPending,
		ReportLocatorRef: "telegram:1:0",
		PayloadJSON:       `{"content":"review"}`,
	}
	created, err := provider.ScheduleRuns().Create(t.Context(), run)
	if err != nil || !created {
		t.Fatalf("Create() = %v, %v, want true, nil", created, err)
	}
	run.RunID = "run-duplicate"
	created, err = provider.ScheduleRuns().Create(t.Context(), run)
	if err != nil || created {
		t.Fatalf("Create(duplicate key) = %v, %v, want false, nil", created, err)
	}
	byKey, found, err := provider.ScheduleRuns().GetByTriggerKey(t.Context(), run.ScheduleID, run.TriggerKey)
	if err != nil || !found || byKey.RunID != "run-1" {
		t.Fatalf("GetByTriggerKey() = %+v, %v, %v, want original run", byKey, found, err)
	}
	if err := provider.ScheduledJobs().Delete(t.Context(), run.ScheduleID); err != nil {
		t.Fatal(err)
	}
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}

	provider, err = NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	history, err := provider.ScheduleRuns().ListBySchedule(t.Context(), run.ScheduleID, time.Time{}, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].RunID != "run-1" || history[0].Trigger != ScheduleRunTriggerManual {
		t.Fatalf("history = %+v, want retained manual run", history)
	}
	if !history[0].RequestedAt.Equal(requestedAt) || history[0].PayloadJSON != run.PayloadJSON ||
		history[0].ReportLocatorRef != run.ReportLocatorRef {
		t.Fatalf("reopened run = %+v, want requested_at and payload retained", history[0])
	}
	history[0].DispatchState = ScheduleRunDispatched
	history[0].ExecutionJobID = "execution-1"
	updated, err := provider.ScheduleRuns().Update(t.Context(), history[0], history[0].Version)
	if err != nil || !updated {
		t.Fatalf("Update() = %v, %v, want true, nil", updated, err)
	}
	updated, err = provider.ScheduleRuns().Update(t.Context(), history[0], history[0].Version)
	if err != nil || updated {
		t.Fatalf("Update(stale version) = %v, %v, want false, nil", updated, err)
	}
	current, found, err := provider.ScheduleRuns().GetByID(t.Context(), "run-1")
	if err != nil || !found || current.DispatchState != ScheduleRunDispatched || current.Version != 2 ||
		current.ReportLocatorRef != run.ReportLocatorRef {
		t.Fatalf("GetByID(after update) = %+v, %v, %v", current, found, err)
	}
	pending, err := provider.ScheduleRuns().ListPending(t.Context(), requestedAt.Add(time.Minute), 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("ListPending(after dispatch) = %+v, %v, want none", pending, err)
	}
}

// This catches a store that silently rewrites managed ownership or dispatches
// a disabled schedule despite the persisted operator selection.
func TestScheduledJobStorePersistsManagedSelection(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	record := ScheduledJobRecord{
		JobID: "managed-review", Source: "managed", Enabled: false, DefinitionVersion: 1,
		TargetKind: "alias", TargetKey: "owner", SessionID: "tg-1-0",
		ChannelType: ChannelTypeTelegram, AddressKey: "1:0", AddressJSON: `{}`,
		Content: "review", ScheduleSpec: "0 9 * * *", Timezone: "UTC",
		Status: ScheduledJobStatusActive, NextRunAt: time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC),
	}
	if err := provider.ScheduledJobs().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	got, found, err := provider.ScheduledJobs().GetByID(t.Context(), record.JobID)
	if err != nil || !found {
		t.Fatalf("GetByID() = %+v, %v, %v", got, found, err)
	}
	if got.Source != "managed" || got.Enabled || got.DefinitionVersion != 1 || got.TargetKind != "alias" || got.TargetKey != "owner" {
		t.Fatalf("managed metadata = %+v", got)
	}
	due, err := provider.ScheduledJobs().ListDue(t.Context(), record.NextRunAt.Add(time.Second), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("disabled schedule due = %+v, %v, want none", due, err)
	}
}

func TestScheduledJobRuntimeUpdateRejectsStaleDefinition(t *testing.T) {
	provider, err := NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	now := time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)
	record := ScheduledJobRecord{JobID: "daily", Source: ScheduledJobSourceManaged,
		Enabled: true, DefinitionVersion: 1, SessionID: "tg-1-0", ChannelType: ChannelTypeTelegram,
		AddressKey: "1:0", AddressJSON: `{}`, Content: "old", ScheduleSpec: "0 9 * * *",
		Status: ScheduledJobStatusActive, NextRunAt: now}
	if err := provider.ScheduledJobs().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	record.Content = "new"
	record.Enabled = false
	record.DefinitionVersion = 2
	if err := provider.ScheduledJobs().Upsert(t.Context(), record); err != nil {
		t.Fatal(err)
	}
	updated, err := provider.ScheduledJobs().UpdateRuntime(t.Context(), ScheduledJobRuntimeUpdate{
		JobID: "daily", DefinitionVersion: 1, ExpectedNextRunAt: now,
		NextRunAt: now.Add(time.Hour), LastDispatchKey: "daily@slot", Status: ScheduledJobStatusActive,
	})
	if err != nil || updated {
		t.Fatalf("stale update = %v, %v", updated, err)
	}
	got, _, err := provider.ScheduledJobs().GetByID(t.Context(), "daily")
	if err != nil || got.Content != "new" || got.Enabled || got.DefinitionVersion != 2 || !got.NextRunAt.Equal(now) {
		t.Fatalf("after stale update = %+v, %v", got, err)
	}
}
