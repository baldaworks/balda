package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

var managedScheduleID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var manualRequestKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// Management owns recurring schedule definitions and selection policy.
type Management struct {
	jobs          state.ScheduledJobStore
	store         state.ScheduleManagementStore
	runs          state.ScheduleRunStore
	executionJobs state.JobLifecycleStore
	resolver      envelopetarget.DestinationResolver
	now           func() time.Time
}

// NewManagement composes the schedule policy with its persistence ports.
func NewManagement(jobs state.ScheduledJobStore, store state.ScheduleManagementStore,
	runs state.ScheduleRunStore, executionJobs state.JobLifecycleStore,
	resolver envelopetarget.DestinationResolver) *Management {
	return &Management{jobs: jobs, store: store, runs: runs, executionJobs: executionJobs,
		resolver: resolver, now: time.Now}
}

// RunNow admits one manual execution, including for a disabled schedule when confirmed.
func (m *Management) RunNow(ctx context.Context, request schedulecmd.RunNow) (schedulecmd.RunItem, error) {
	if err := m.store.CheckAuthority(ctx, request.Authority); err != nil {
		return schedulecmd.RunItem{}, err
	}
	if !manualRequestKey.MatchString(request.RequestKey) {
		return schedulecmd.RunItem{}, schedulecmd.ErrInvalid
	}
	job, err := m.get(ctx, request.ID)
	if err != nil {
		return schedulecmd.RunItem{}, err
	}
	triggerKey := "manual:" + request.RequestKey
	if existing, found, err := m.runs.GetByTriggerKey(ctx, job.JobID, triggerKey); err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	} else if found {
		return m.projectRun(ctx, existing)
	}
	if job.Deleted {
		return schedulecmd.RunItem{}, schedulecmd.ErrNotFound
	}
	if !job.Enabled && !request.ConfirmDisabled {
		return schedulecmd.RunItem{}, schedulecmd.ErrConflict
	}
	payload, err := json.Marshal(job)
	if err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	now := m.now().UTC()
	run := state.ScheduleRunRecord{RunID: uuid.NewString(), ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerManual, TriggerKey: triggerKey,
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload)}
	audit := scheduleAudit(job.JobID, request.Authority)
	audit.Action = usercmd.AuditActionScheduleRunRequested
	_, err = m.store.AdmitManualRun(ctx, state.ScheduleManualAdmission{Run: run,
		ExpectedVersion: job.DefinitionVersion, ConfirmDisabled: request.ConfirmDisabled,
		Authority: request.Authority, Audit: audit})
	if err != nil {
		return schedulecmd.RunItem{}, err
	}
	stored, found, err := m.runs.GetByTriggerKey(ctx, job.JobID, triggerKey)
	if err != nil || !found {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	return m.projectRun(ctx, stored)
}

// History returns the newest bounded runs, including for archived schedules.
func (m *Management) History(ctx context.Context, id string, beforeAt time.Time, beforeID string,
	limit int, authority schedulecmd.Authority) ([]schedulecmd.RunItem, error) {
	if err := m.store.CheckAuthority(ctx, authority); err != nil {
		return nil, err
	}
	if _, err := m.get(ctx, id); err != nil {
		return nil, err
	}
	runs, err := m.runs.ListBySchedule(ctx, id, beforeAt, beforeID, limit)
	if err != nil {
		return nil, schedulecmd.ErrUnavailable
	}
	items := make([]schedulecmd.RunItem, 0, len(runs))
	for _, run := range runs {
		item, err := m.projectRun(ctx, run)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (m *Management) projectRun(ctx context.Context, run state.ScheduleRunRecord) (schedulecmd.RunItem, error) {
	item := schedulecmd.RunItem{ID: run.RunID, Trigger: run.Trigger,
		RequestedAt: run.RequestedAt, DueAt: run.DueAt,
		State: run.DispatchState, SafeFailureCode: run.SafeFailureCode}
	if run.DispatchState == state.ScheduleRunPending {
		item.State = "queued"
	}
	if run.DispatchState != state.ScheduleRunDispatched || run.ExecutionJobID == "" || m.executionJobs == nil {
		return item, nil
	}
	job, found, err := m.executionJobs.GetJob(ctx, run.ExecutionJobID)
	if err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	if !found {
		return item, nil
	}
	switch job.Status {
	case state.JobStatusCompleted:
		item.State, item.CompletedAt = "succeeded", job.CompletedAt
	case state.JobStatusFailed, state.JobStatusDeadLettered:
		item.State, item.CompletedAt, item.SafeFailureCode = "failed", job.CompletedAt, "execution_failed"
	case state.JobStatusCanceled:
		item.State, item.CompletedAt = "canceled", job.CanceledAt
	default:
		item.State = "running"
	}
	return item, nil
}

func (m *Management) Inventory(ctx context.Context, authority schedulecmd.Authority) ([]schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, authority); err != nil {
		return nil, err
	}
	records, err := m.jobs.List(ctx)
	if err != nil {
		return nil, schedulecmd.ErrUnavailable
	}
	items := make([]schedulecmd.Item, 0, len(records))
	for _, record := range records {
		if record.Source == state.ScheduledJobSourceInternal || record.Deleted {
			continue
		}
		item := scheduleItem(record)
		item.Definition.Content = ""
		items = append(items, item)
	}
	return items, nil
}

func (m *Management) Get(ctx context.Context, id string, authority schedulecmd.Authority) (schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, authority); err != nil {
		return schedulecmd.Item{}, err
	}
	record, err := m.get(ctx, id)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	return scheduleItem(record), nil
}

func (m *Management) Create(ctx context.Context, request schedulecmd.Create) (schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, request.Authority); err != nil {
		return schedulecmd.Item{}, err
	}
	record, err := m.build(ctx, request.Definition)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	record.Source = state.ScheduledJobSourceManaged
	record.Enabled = true
	record.DefinitionVersion = 1
	if err := m.store.Save(ctx, state.ScheduleMutation{Kind: state.ScheduleCreate, Record: record,
		Authority: request.Authority, Audit: scheduleAudit(record.JobID, request.Authority)}); err != nil {
		return schedulecmd.Item{}, err
	}
	return scheduleItem(record), nil
}

func (m *Management) Update(ctx context.Context, request schedulecmd.Update) (schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, request.Authority); err != nil {
		return schedulecmd.Item{}, err
	}
	previous, err := m.getManaged(ctx, request.ID, request.ExpectedVersion)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	if request.Definition.ID != request.ID {
		return schedulecmd.Item{}, schedulecmd.ErrInvalid
	}
	record, err := m.build(ctx, request.Definition)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	record.Source = state.ScheduledJobSourceManaged
	record.Enabled = previous.Enabled
	record.DefinitionVersion = previous.DefinitionVersion + 1
	record.LastRunAt = previous.LastRunAt
	if err := m.store.Save(ctx, state.ScheduleMutation{Kind: state.ScheduleEdit, Record: record,
		ExpectedVersion: request.ExpectedVersion, Authority: request.Authority,
		Audit: scheduleAudit(record.JobID, request.Authority)}); err != nil {
		return schedulecmd.Item{}, err
	}
	return scheduleItem(record), nil
}

func (m *Management) SetEnabled(ctx context.Context, request schedulecmd.ChangeSelection) (schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, request.Authority); err != nil {
		return schedulecmd.Item{}, err
	}
	record, err := m.getManaged(ctx, request.ID, request.ExpectedVersion)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	record.Enabled = request.Enabled
	record.DefinitionVersion++
	if request.Enabled {
		record.NextRunAt, err = nextRunAtFromSpec(record.ScheduleSpec, m.now().UTC())
		if err != nil {
			return schedulecmd.Item{}, schedulecmd.ErrInvalid
		}
		record.Status = state.ScheduledJobStatusActive
	}
	if err := m.store.Save(ctx, state.ScheduleMutation{Kind: state.ScheduleSelection, Record: record,
		ExpectedVersion: request.ExpectedVersion, Authority: request.Authority,
		Audit: scheduleAudit(record.JobID, request.Authority)}); err != nil {
		return schedulecmd.Item{}, err
	}
	return scheduleItem(record), nil
}

func (m *Management) Delete(ctx context.Context, request schedulecmd.Delete) (schedulecmd.Item, error) {
	if err := m.store.CheckAuthority(ctx, request.Authority); err != nil {
		return schedulecmd.Item{}, err
	}
	record, err := m.getManaged(ctx, request.ID, request.ExpectedVersion)
	if err != nil {
		return schedulecmd.Item{}, err
	}
	record.Enabled = false
	record.Deleted = true
	record.DefinitionVersion++
	if err := m.store.Save(ctx, state.ScheduleMutation{Kind: state.ScheduleDelete, Record: record,
		ExpectedVersion: request.ExpectedVersion, Authority: request.Authority,
		Audit: scheduleAudit(record.JobID, request.Authority)}); err != nil {
		return schedulecmd.Item{}, err
	}
	return scheduleItem(record), nil
}

func (m *Management) getManaged(ctx context.Context, id string, version uint64) (state.ScheduledJobRecord, error) {
	record, err := m.get(ctx, id)
	if err != nil {
		return record, err
	}
	if record.Source != state.ScheduledJobSourceManaged {
		return record, schedulecmd.ErrForbidden
	}
	if record.Deleted || record.DefinitionVersion != version || version == 0 {
		return record, schedulecmd.ErrConflict
	}
	return record, nil
}

func (m *Management) get(ctx context.Context, id string) (state.ScheduledJobRecord, error) {
	record, found, err := m.jobs.GetByID(ctx, strings.TrimSpace(id))
	if err != nil {
		return state.ScheduledJobRecord{}, schedulecmd.ErrUnavailable
	}
	if !found || record.Source == state.ScheduledJobSourceInternal {
		return state.ScheduledJobRecord{}, schedulecmd.ErrNotFound
	}
	return record, nil
}

func (m *Management) build(ctx context.Context, definition schedulecmd.Definition) (state.ScheduledJobRecord, error) {
	id := strings.TrimSpace(definition.ID)
	cron := strings.TrimSpace(definition.Cron)
	content := strings.TrimSpace(definition.Content)
	if !managedScheduleID.MatchString(id) || len(strings.Fields(cron)) != 5 || len(cron) > 128 ||
		content == "" || len(content) > 16384 || len(definition.Target.Kind) > 32 || len(definition.Target.Key) > 512 {
		return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
	}
	next, err := nextRunAtFromSpec(cron, m.now().UTC())
	if err != nil {
		return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
	}
	target, err := envelopetarget.Resolve(ctx, m.resolver, envelopetarget.Target{Target: definition.Target.Kind, Key: definition.Target.Key})
	if err != nil {
		return state.ScheduledJobRecord{}, scheduleTargetError(err)
	}
	r := state.ScheduledJobRecord{JobID: id, TargetKind: definition.Target.Kind, TargetKey: definition.Target.Key,
		SessionID: target.Locator.SessionID, ChannelType: target.Locator.ChannelType,
		AddressKey: target.Locator.AddressKey, AddressJSON: target.Locator.AddressJSON,
		Content: content, ScheduleSpec: cron, Timezone: "UTC", Status: state.ScheduledJobStatusActive,
		MaxRetries: defaultSchedulerMaxRetries, NextRunAt: next}
	if definition.ReportTo != nil {
		if len(definition.ReportTo.Kind) > 32 || len(definition.ReportTo.Key) > 512 {
			return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
		}
		resolved, err := envelopetarget.Resolve(ctx, m.resolver, envelopetarget.Target{Target: definition.ReportTo.Kind, Key: definition.ReportTo.Key})
		if err != nil {
			return state.ScheduledJobRecord{}, scheduleTargetError(err)
		}
		r.ReportToEnabled = true
		r.ReportToTargetKind = definition.ReportTo.Kind
		r.ReportToTargetKey = definition.ReportTo.Key
		r.ReportToSessionID = resolved.Locator.SessionID
		r.ReportToChannelType = resolved.Locator.ChannelType
		r.ReportToAddressKey = resolved.Locator.AddressKey
		r.ReportToAddressJSON = resolved.Locator.AddressJSON
	}
	return r, nil
}

func scheduleTargetError(err error) error {
	if errors.Is(err, envelopetarget.ErrResolutionUnavailable) {
		return schedulecmd.ErrUnavailable
	}
	return schedulecmd.ErrInvalid
}

func scheduleItem(r state.ScheduledJobRecord) schedulecmd.Item {
	d := schedulecmd.Definition{ID: r.JobID, Cron: r.ScheduleSpec, Content: r.Content,
		Target: schedulecmd.Target{Kind: r.TargetKind, Key: r.TargetKey}}
	if r.ReportToEnabled {
		d.ReportTo = &schedulecmd.Target{Kind: r.ReportToTargetKind, Key: r.ReportToTargetKey}
	}
	return schedulecmd.Item{Definition: d, Source: r.Source, Enabled: r.Enabled, Deleted: r.Deleted,
		Version: r.DefinitionVersion, Status: r.Status, NextRunAt: r.NextRunAt, LastRunAt: r.LastRunAt}
}

func scheduleAudit(id string, authority schedulecmd.Authority) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: uuid.NewString(), Action: usercmd.AuditActionScheduleDefinitionChanged,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.UserID,
		ActorSessionID: authority.SessionID, TargetType: usercmd.AuditTargetSchedule,
		TargetID: id, Source: "backoffice", OccurredAt: authority.At}
}
