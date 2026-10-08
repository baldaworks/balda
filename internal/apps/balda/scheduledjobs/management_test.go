package scheduledjobs

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	baldatelegram "github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

const (
	testTelegramReportLocator = "telegram:9001:0"
	testTelegramAddressKey    = "9001:0"
	testManagedScheduleAlias  = "main_chat"
)

type recordingScheduleManagementStore struct {
	jobs state.ScheduledJobStore
	runs state.ScheduleRunStore
}

func (*recordingScheduleManagementStore) CheckAuthority(context.Context, schedulecmd.Authority) error {
	return nil
}
func (s *recordingScheduleManagementStore) Save(ctx context.Context, m state.ScheduleMutation) error {
	return s.jobs.Upsert(ctx, m.Record)
}
func (s *recordingScheduleManagementStore) AdmitManualRun(ctx context.Context, admission state.ScheduleManualAdmission) (bool, error) {
	return s.runs.Create(ctx, admission.Run)
}

func TestManagementManualRunIsIdempotentAndHistoryIsSafe(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = provider.Close() }()
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	job := state.ScheduledJobRecord{JobID: "daily", Source: state.ScheduledJobSourceConfig,
		Enabled: false, DefinitionVersion: 1, SessionID: "tg-9001-0",
		ChannelType: state.ChannelTypeTelegram, AddressKey: testTelegramAddressKey, AddressJSON: `{}`,
		Content: "private content", ScheduleSpec: "0 9 * * *", Status: state.ScheduledJobStatusActive,
		NextRunAt: now.Add(time.Hour)}
	if err := provider.ScheduledJobs().Upsert(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	store := &recordingScheduleManagementStore{jobs: provider.ScheduledJobs(), runs: provider.ScheduleRuns()}
	m := NewManagement(provider.ScheduledJobs(), store, provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	m.now = func() time.Time { return now }
	authority := schedulecmd.Authority{UserID: "admin", SessionID: "session", At: now}
	request := schedulecmd.RunNow{ID: job.JobID, RequestKey: "nonce-123", Authority: authority}
	if _, err := m.RunNow(t.Context(), request); !errors.Is(err, schedulecmd.ErrConflict) {
		t.Fatalf("unconfirmed disabled run = %v", err)
	}
	request.ConfirmDisabled = true
	first, err := m.RunNow(t.Context(), request)
	if err != nil || first.ID == "" || first.State != "queued" {
		t.Fatalf("first run = %+v, %v", first, err)
	}
	second, err := m.RunNow(t.Context(), request)
	if err != nil || second.ID != first.ID {
		t.Fatalf("duplicate run = %+v, %v", second, err)
	}
	current, _, err := provider.ScheduledJobs().GetByID(t.Context(), job.JobID)
	if err != nil || current.Enabled || !current.NextRunAt.Equal(job.NextRunAt) {
		t.Fatalf("manual run moved cron = %+v, %v", current, err)
	}
	runs, err := m.History(t.Context(), job.JobID, time.Time{}, "", 10, authority)
	if err != nil || len(runs) != 1 || runs[0].ID != first.ID || runs[0].SafeFailureCode != "" {
		t.Fatalf("history = %+v, %v", runs, err)
	}
	stored, found, err := provider.ScheduleRuns().GetByID(t.Context(), first.ID)
	if err != nil || !found {
		t.Fatalf("load run = %+v, %v", stored, err)
	}
	stored.DispatchState = state.ScheduleRunDispatched
	stored.ExecutionJobID = "execution-1"
	stored.DispatchedAt = now
	if updated, err := provider.ScheduleRuns().Update(t.Context(), stored, stored.Version); err != nil || !updated {
		t.Fatalf("update run = %v, %v", updated, err)
	}
	if created, err := provider.Jobs().CreateJob(t.Context(), state.JobRecord{ID: "execution-1",
		SessionID: job.SessionID, Objective: "private content", Status: state.JobStatusFailed,
		Error: "private provider failure", CompletedAt: now.Add(time.Minute)}); err != nil || !created {
		t.Fatalf("create execution = %v, %v", created, err)
	}
	runs, err = m.History(t.Context(), job.JobID, time.Time{}, "", 10, authority)
	if err != nil || len(runs) != 1 || runs[0].State != runStateFailed ||
		runs[0].SafeFailureCode != runFailureExecution {
		t.Fatalf("safe failed history = %+v, %v", runs, err)
	}
	job.Deleted = true
	job.Enabled = false
	if err := provider.ScheduledJobs().Upsert(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	runs, err = m.History(t.Context(), job.JobID, time.Time{}, "", 10, authority)
	if err != nil || len(runs) != 1 {
		t.Fatalf("archived history = %+v, %v", runs, err)
	}
}

func TestManagedScheduleManualRunFreezesAliasAndDuplicateSelection(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := &recordingScheduleManagementStore{jobs: provider.ScheduledJobs(), runs: provider.ScheduleRuns()}
	manager := NewManagement(provider.ScheduledJobs(), store, provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	resolver := &mutableScheduleAliasResolver{locator: baldatelegram.NewLocator(9001, 0)}
	manager.resolver = resolver
	manager.now = func() time.Time { return time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC) }
	definition := schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Content: "review", Alias: testManagedScheduleAlias}
	created, err := manager.Create(t.Context(), schedulecmd.Create{Definition: definition})
	if err != nil || created.Definition.Alias != testManagedScheduleAlias {
		t.Fatalf("created alias schedule = %+v, err=%v", created, err)
	}
	first, err := manager.RunNow(t.Context(), schedulecmd.RunNow{ID: "daily", RequestKey: "request-111"})
	if err != nil {
		t.Fatal(err)
	}
	run, found, err := provider.ScheduleRuns().GetByID(t.Context(), first.ID)
	if err != nil || !found || run.ReportLocatorRef != testTelegramReportLocator {
		t.Fatalf("first run = %+v, found=%t err=%v", run, found, err)
	}
	resolver.locator = baldatelegram.NewLocator(9002, 0)
	duplicate, err := manager.RunNow(t.Context(), schedulecmd.RunNow{ID: "daily", RequestKey: "request-111"})
	if err != nil || duplicate.ID != first.ID || resolver.calls != 1 {
		t.Fatalf("duplicate = %+v, calls=%d err=%v", duplicate, resolver.calls, err)
	}
	second, err := manager.RunNow(t.Context(), schedulecmd.RunNow{ID: "daily", RequestKey: "request-222"})
	if err != nil || second.ID == first.ID {
		t.Fatalf("second = %+v, err=%v", second, err)
	}
	run, found, err = provider.ScheduleRuns().GetByID(t.Context(), second.ID)
	if err != nil || !found || run.ReportLocatorRef != "telegram:9002:0" {
		t.Fatalf("second run = %+v, found=%t err=%v", run, found, err)
	}
}

func TestManualRunWithMissingAliasRecordsFailureWithoutChangingCron(t *testing.T) {
	provider, err := state.NewSQLiteProvider(t.Context(), filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := &recordingScheduleManagementStore{jobs: provider.ScheduledJobs(), runs: provider.ScheduleRuns()}
	manager := NewManagement(provider.ScheduledJobs(), store, provider.ScheduleRuns(), provider.Jobs(), provider.Jobs())
	manager.resolver = &mutableScheduleAliasResolver{}
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	if _, err := manager.Create(t.Context(), schedulecmd.Create{Definition: schedulecmd.Definition{
		ID: "daily", Cron: "0 9 * * *", Content: "review", Alias: testManagedScheduleAlias,
	}}); err != nil {
		t.Fatal(err)
	}
	before, _, err := provider.ScheduledJobs().GetByID(t.Context(), "daily")
	if err != nil {
		t.Fatal(err)
	}
	result, err := manager.RunNow(t.Context(), schedulecmd.RunNow{ID: "daily", RequestKey: "request-111"})
	if err != nil || result.State != state.ScheduleRunFailed || result.SafeFailureCode != runFailureReportAliasUnavailable {
		t.Fatalf("missing alias run = %+v, err=%v", result, err)
	}
	after, _, err := provider.ScheduledJobs().GetByID(t.Context(), "daily")
	if err != nil || !after.NextRunAt.Equal(before.NextRunAt) || after.LastDispatchKey != before.LastDispatchKey {
		t.Fatalf("manual failure moved cron cursor: before=%+v after=%+v err=%v", before, after, err)
	}
}

func TestManagementProjectsPrepublicationCompletionTime(t *testing.T) {
	completed := time.Date(2026, 10, 8, 9, 5, 0, 0, time.UTC)
	m := &Management{}
	for _, stateName := range []string{state.ScheduleRunFailed, state.ScheduleRunCanceled} {
		t.Run(stateName, func(t *testing.T) {
			item, err := m.projectRun(t.Context(), state.ScheduleRunRecord{
				RunID: "run", Trigger: state.ScheduleRunTriggerCron,
				DispatchState: stateName, UpdatedAt: completed,
			})
			if err != nil || item.State != stateName || !item.CompletedAt.Equal(completed) {
				t.Fatalf("terminal run = %+v, %v", item, err)
			}
		})
	}
}

func TestManagementRunProjectionShowsFrozenReportLocator(t *testing.T) {
	manager := &Management{}
	item, err := manager.projectRun(t.Context(), state.ScheduleRunRecord{
		RunID: "run-1", Trigger: state.ScheduleRunTriggerManual,
		DispatchState: state.ScheduleRunPending, ReportLocatorRef: testTelegramReportLocator,
	})
	if err != nil || item.ReportLocatorRef != testTelegramReportLocator {
		t.Fatalf("projected run = %+v, err=%v", item, err)
	}
}

func TestManagementLifecyclePreservesDisabledSelectionAndArchive(t *testing.T) {
	ctx := t.Context()
	jobs := newSchedulerJobStore(t)
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, nil, nil, nil)
	m.now = func() time.Time { return now }
	authority := schedulecmd.Authority{UserID: "admin", SessionID: "session", At: now}
	definition := schedulecmd.Definition{ID: "daily-review", Cron: "0 9 * * *", Content: "review",
		Locator: testTelegramReportLocator}
	created, err := m.Create(ctx, schedulecmd.Create{Definition: definition, Authority: authority})
	if err != nil || !created.Enabled || created.Version != 1 || created.Source != state.ScheduledJobSourceManaged ||
		!created.NextRunAt.Equal(time.Date(2026, 10, 8, 9, 0, 0, 0, time.UTC)) {
		t.Fatalf("created = %+v, %v", created, err)
	}
	inventory, err := m.Inventory(ctx, authority)
	if err != nil || len(inventory) != 1 || inventory[0].Definition.Content != "" {
		t.Fatalf("inventory leaked content = %+v, %v", inventory, err)
	}
	disabled, err := m.SetEnabled(ctx, schedulecmd.ChangeSelection{ID: definition.ID, ExpectedVersion: 1, Enabled: false, Authority: authority})
	if err != nil || disabled.Enabled || disabled.Version != 2 {
		t.Fatalf("disabled = %+v, %v", disabled, err)
	}
	definition.Content = "updated review"
	updated, err := m.Update(ctx, schedulecmd.Update{ID: definition.ID, ExpectedVersion: 2, Definition: definition, Authority: authority})
	if err != nil || updated.Enabled || updated.Version != 3 || updated.Definition.Content != definition.Content {
		t.Fatalf("updated = %+v, %v", updated, err)
	}
	if _, err := m.Update(ctx, schedulecmd.Update{ID: definition.ID, ExpectedVersion: 2, Definition: definition, Authority: authority}); !errors.Is(err, schedulecmd.ErrConflict) {
		t.Fatalf("stale update = %v", err)
	}
	archived, err := m.Delete(ctx, schedulecmd.Delete{ID: definition.ID, ExpectedVersion: 3, Authority: authority})
	if err != nil || !archived.Deleted || archived.Enabled || archived.Version != 4 {
		t.Fatalf("archived = %+v, %v", archived, err)
	}
	inventory, err = m.Inventory(ctx, authority)
	if err != nil || len(inventory) != 0 {
		t.Fatalf("inventory after archive = %+v, %v", inventory, err)
	}
}

func TestManagementCreatesScheduleWithoutReportLocator(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, nil, nil, nil)
	authority := schedulecmd.Authority{At: time.Now().UTC()}
	item, err := m.Create(t.Context(), schedulecmd.Create{Definition: schedulecmd.Definition{
		ID: "local-only", Cron: "0 9 * * *", Content: "review local state",
	}, Authority: authority})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if item.Definition.Locator != "" {
		t.Fatalf("locator = %q, want omitted", item.Definition.Locator)
	}
	stored, found, err := jobs.GetByID(t.Context(), "local-only")
	if err != nil || !found || stored.ReportToEnabled {
		t.Fatalf("stored schedule = %+v, found=%v, err=%v", stored, found, err)
	}
}

func TestManagementRejectsInvalidDefinitionAndConfigMutation(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	now := time.Date(2026, 10, 8, 8, 0, 0, 0, time.UTC)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, nil, nil, nil)
	m.now = func() time.Time { return now }
	authority := schedulecmd.Authority{At: now}
	definition := schedulecmd.Definition{ID: "bad/id", Cron: "@every 1m", Content: "review",
		Locator: testTelegramReportLocator}
	if _, err := m.Create(t.Context(), schedulecmd.Create{Definition: definition, Authority: authority}); !errors.Is(err, schedulecmd.ErrInvalid) {
		t.Fatalf("invalid definition = %v", err)
	}
	config := state.ScheduledJobRecord{JobID: "config-daily", Source: state.ScheduledJobSourceConfig,
		Enabled: true, DefinitionVersion: 1, SessionID: "tg-9001-0", ChannelType: state.ChannelTypeTelegram,
		AddressKey: testTelegramAddressKey, AddressJSON: `{}`, Content: "config", ScheduleSpec: "0 9 * * *",
		NextRunAt: now.Add(time.Hour)}
	if err := jobs.Upsert(t.Context(), config); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetEnabled(t.Context(), schedulecmd.ChangeSelection{ID: config.JobID, ExpectedVersion: 1, Authority: authority}); !errors.Is(err, schedulecmd.ErrForbidden) {
		t.Fatalf("config mutation = %v", err)
	}
}

func TestManagementValidatesSuppliedPublicReportLocator(t *testing.T) {
	jobs := newSchedulerJobStore(t)
	m := NewManagement(jobs, &recordingScheduleManagementStore{jobs: jobs}, nil, nil, nil)
	base := schedulecmd.Definition{ID: "daily", Cron: "0 9 * * *", Content: "review"}
	for _, locator := range []string{"owner", "telegram:bad", "unknown:key"} {
		definition := base
		definition.Locator = locator
		if _, err := m.Create(t.Context(), schedulecmd.Create{Definition: definition}); !errors.Is(err, schedulecmd.ErrInvalid) {
			t.Fatalf("locator %q: Create() = %v, want invalid", locator, err)
		}
	}
	if records, err := jobs.List(t.Context()); err != nil || len(records) != 0 {
		t.Fatalf("invalid locator wrote definitions: %+v, %v", records, err)
	}
	base.Locator = testTelegramReportLocator
	item, err := m.Create(t.Context(), schedulecmd.Create{Definition: base})
	if err != nil || item.Definition.Locator != base.Locator {
		t.Fatalf("Create() = %+v, %v", item, err)
	}
	stored, found, err := jobs.GetByID(t.Context(), base.ID)
	if err != nil || !found || !stored.ReportToEnabled || stored.AddressKey != testTelegramAddressKey || stored.ReportToAddressKey != testTelegramAddressKey {
		t.Fatalf("stored locator = %+v, %v", stored, err)
	}
}
