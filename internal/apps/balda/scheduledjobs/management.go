package scheduledjobs

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/google/uuid"
)

const (
	runStateSucceeded                = "succeeded"
	runStateFailed                   = "failed"
	runStateCanceled                 = "canceled"
	runFailureExecution              = "execution_failed"
	runFailureReportAliasUnavailable = "report_alias_unavailable"
)

var managedScheduleID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var managedScheduleAlias = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,63}$`)
var manualRequestKey = regexp.MustCompile(`^[A-Za-z0-9_-]{8,128}$`)

// Management owns recurring schedule definitions and selection policy.
type Management struct {
	jobs          state.ScheduledJobStore
	store         state.ScheduleManagementStore
	runs          state.ScheduleRunStore
	executionJobs state.JobLifecycleStore
	deliveries    state.DeliveryStore
	resolver      envelopetarget.DestinationResolver
	now           func() time.Time
}

// NewManagement composes the schedule policy with its persistence ports.
func NewManagement(jobs state.ScheduledJobStore, store state.ScheduleManagementStore,
	runs state.ScheduleRunStore, executionJobs state.JobLifecycleStore, deliveries state.DeliveryStore) *Management {
	return &Management{jobs: jobs, store: store, runs: runs, executionJobs: executionJobs,
		deliveries: deliveries, now: time.Now}
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
	selected := job
	reportRef, resolutionErr := selectScheduleReport(ctx, m.resolver, &selected)
	if resolutionErr != nil && !errors.Is(resolutionErr, envelopetarget.ErrDestinationUnavailable) {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	payload, err := json.Marshal(selected)
	if err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	now := m.now().UTC()
	run := state.ScheduleRunRecord{RunID: uuid.NewString(), ScheduleID: job.JobID,
		Trigger: state.ScheduleRunTriggerManual, TriggerKey: triggerKey,
		DefinitionVersion: job.DefinitionVersion, RequestedAt: now,
		DispatchState: state.ScheduleRunPending, PayloadJSON: string(payload),
		ReportLocatorRef: reportRef}
	if resolutionErr != nil {
		run.DispatchState = state.ScheduleRunFailed
		run.SafeFailureCode = runFailureReportAliasUnavailable
		run.Attempts = 1
	}
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

// RunDetail returns one run's frozen instruction and provider result.
func (m *Management) RunDetail(ctx context.Context, scheduleID, runID string,
	authority schedulecmd.Authority) (schedulecmd.RunDetail, error) {
	if err := m.store.CheckAuthority(ctx, authority); err != nil {
		return schedulecmd.RunDetail{}, err
	}
	if _, err := m.get(ctx, scheduleID); err != nil {
		return schedulecmd.RunDetail{}, err
	}
	run, found, err := m.runs.GetByID(ctx, runID)
	if err != nil {
		return schedulecmd.RunDetail{}, schedulecmd.ErrUnavailable
	}
	if !found || run.ScheduleID != scheduleID {
		return schedulecmd.RunDetail{}, schedulecmd.ErrNotFound
	}
	item, err := m.projectRun(ctx, run)
	if err != nil {
		return schedulecmd.RunDetail{}, err
	}
	detail := schedulecmd.RunDetail{Run: item}
	var snapshot state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &snapshot); err != nil {
		return schedulecmd.RunDetail{}, schedulecmd.ErrUnavailable
	}
	detail.Input = snapshot.Content
	if run.ExecutionJobID != "" && m.executionJobs != nil {
		job, found, err := m.executionJobs.GetJob(ctx, run.ExecutionJobID)
		if err != nil {
			return schedulecmd.RunDetail{}, schedulecmd.ErrUnavailable
		}
		if found {
			detail.Output = job.Result
		}
	}
	if detail.Output == "" && run.ExecutionJobID != "" && m.deliveries != nil {
		delivery, found, err := m.deliveries.FinalDelivery(ctx, run.ExecutionJobID)
		if err != nil {
			return schedulecmd.RunDetail{}, schedulecmd.ErrUnavailable
		}
		if found && delivery.Status == state.DeliveryStatusSent {
			var payload deliverycmd.Payload
			if err := json.Unmarshal([]byte(delivery.Payload), &payload); err != nil {
				return schedulecmd.RunDetail{}, schedulecmd.ErrUnavailable
			}
			detail.Output = payload.Text
		}
	}
	return detail, nil
}

func (m *Management) projectRun(ctx context.Context, run state.ScheduleRunRecord) (schedulecmd.RunItem, error) {
	item := schedulecmd.RunItem{ID: run.RunID, Trigger: run.Trigger,
		RequestedAt: run.RequestedAt, DueAt: run.DueAt,
		State: run.DispatchState, SafeFailureCode: run.SafeFailureCode,
		ReportLocatorRef: run.ReportLocatorRef}
	if run.DispatchState == state.ScheduleRunPending {
		item.State = "queued"
	}
	if run.DispatchState == state.ScheduleRunFailed || run.DispatchState == state.ScheduleRunCanceled {
		item.CompletedAt = run.UpdatedAt
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
	var snapshot state.ScheduledJobRecord
	if err := json.Unmarshal([]byte(run.PayloadJSON), &snapshot); err != nil {
		return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
	}
	if job.SessionID == turncmd.ScheduledExecutionSessionID(run.ExecutionJobID) &&
		snapshot.ReportToEnabled && m.deliveries != nil {
		switch job.Status {
		case state.JobStatusCompleted, state.JobStatusFailed, state.JobStatusCanceled, state.JobStatusDeadLettered:
		default:
			item.State = "running"
			return item, nil
		}
		delivery, present, err := m.deliveries.FinalDelivery(ctx, run.ExecutionJobID)
		if err != nil {
			return schedulecmd.RunItem{}, schedulecmd.ErrUnavailable
		}
		if present && delivery.Status == state.DeliveryStatusSent {
			item.CompletedAt = delivery.SentAt
			if job.Status == state.JobStatusCompleted {
				item.State = runStateSucceeded
			} else {
				item.State, item.SafeFailureCode = runStateFailed, runFailureExecution
			}
			return item, nil
		}
		if present && delivery.Status == state.DeliveryStatusFailed && delivery.Error == "permanent" {
			item.State, item.CompletedAt, item.SafeFailureCode = runStateFailed, delivery.UpdatedAt, "delivery_failed"
			return item, nil
		}
		if !present && job.Status == state.JobStatusCanceled {
			item.State, item.CompletedAt = runStateCanceled, job.CanceledAt
			return item, nil
		}
		if !present && job.Status == state.JobStatusDeadLettered {
			item.State, item.CompletedAt, item.SafeFailureCode = runStateFailed, job.CompletedAt, runFailureExecution
			return item, nil
		}
		item.State = "report_pending"
		return item, nil
	}
	switch job.Status {
	case state.JobStatusCompleted:
		item.State, item.CompletedAt = runStateSucceeded, job.CompletedAt
	case state.JobStatusFailed, state.JobStatusDeadLettered:
		item.State, item.CompletedAt, item.SafeFailureCode = runStateFailed, job.CompletedAt, runFailureExecution
	case state.JobStatusCanceled:
		item.State, item.CompletedAt = runStateCanceled, job.CanceledAt
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
	record, err := m.build(request.Definition)
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
	record, err := m.build(request.Definition)
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

func (m *Management) build(definition schedulecmd.Definition) (state.ScheduledJobRecord, error) {
	id := strings.TrimSpace(definition.ID)
	cron := strings.TrimSpace(definition.Cron)
	content := strings.TrimSpace(definition.Content)
	if !managedScheduleID.MatchString(id) || len(strings.Fields(cron)) != 5 || len(cron) > 128 ||
		content == "" || len(content) > 16384 || len(definition.Locator) > 512 ||
		(definition.Alias != "" && definition.Locator != "") {
		return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
	}
	next, err := nextRunAtFromSpec(cron, m.now().UTC())
	if err != nil {
		return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
	}
	r := state.ScheduledJobRecord{JobID: id,
		Content: content, ScheduleSpec: cron, Timezone: "UTC", Status: state.ScheduledJobStatusActive,
		MaxRetries: defaultSchedulerMaxRetries, NextRunAt: next}
	if definition.Alias != "" {
		alias := strings.TrimSpace(definition.Alias)
		if !validScheduleAliasName(alias) {
			return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
		}
		r.ReportToEnabled = true
		r.ReportToTargetKind = envelopetarget.TargetManagedAlias
		r.ReportToTargetKey = alias
		return r, nil
	}
	if strings.TrimSpace(definition.Locator) == "" {
		return r, nil
	}
	target, err := locatorref.Parse(definition.Locator)
	if err != nil {
		return state.ScheduledJobRecord{}, schedulecmd.ErrInvalid
	}
	canonical := locatorref.Format(target)
	r.TargetKind = envelopetarget.TargetLocator
	r.TargetKey = canonical
	r.SessionID = target.SessionID
	r.ChannelType = target.ChannelType
	r.AddressKey = target.AddressKey
	r.AddressJSON = target.AddressJSON
	r.ReportToEnabled = true
	r.ReportToTargetKind = envelopetarget.TargetLocator
	r.ReportToTargetKey = canonical
	r.ReportToSessionID = target.SessionID
	r.ReportToChannelType = target.ChannelType
	r.ReportToAddressKey = target.AddressKey
	r.ReportToAddressJSON = target.AddressJSON
	return r, nil
}

func validScheduleAliasName(name string) bool {
	return managedScheduleAlias.MatchString(name) && name != deliverycmd.RoleOwner &&
		name != deliverycmd.RoleCollaborator
}

func scheduleItem(r state.ScheduledJobRecord) schedulecmd.Item {
	locator, alias := "", ""
	if r.ReportToEnabled && r.ReportToTargetKind == envelopetarget.TargetManagedAlias {
		alias = r.ReportToTargetKey
	} else if r.ReportToEnabled {
		locator = r.ReportToChannelType + ":" + r.ReportToAddressKey
	}
	d := schedulecmd.Definition{ID: r.JobID, Cron: r.ScheduleSpec, Content: r.Content,
		Locator: locator, Alias: alias}
	return schedulecmd.Item{Definition: d, Source: r.Source, Enabled: r.Enabled, Deleted: r.Deleted,
		Version: r.DefinitionVersion, Status: r.Status, NextRunAt: r.NextRunAt, LastRunAt: r.LastRunAt}
}

func scheduleAudit(id string, authority schedulecmd.Authority) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: uuid.NewString(), Action: usercmd.AuditActionScheduleDefinitionChanged,
		Outcome: usercmd.AuditOutcomeSucceeded, ActorUserID: authority.UserID,
		ActorSessionID: authority.SessionID, TargetType: usercmd.AuditTargetSchedule,
		TargetID: id, Source: "backoffice", OccurredAt: authority.At}
}
