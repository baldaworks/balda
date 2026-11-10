package managementhttp

import (
	"context"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
)

const scheduleRunPageSize = 20

func (s strictServer) ListSchedules(ctx context.Context, _ managementv1.ListSchedulesRequestObject) (managementv1.ListSchedulesResponseObject, error) {
	items, err := s.schedules.Inventory(ctx, scheduleAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.ScheduleInventory{Items: make([]managementv1.ScheduleSummaryItem, 0, len(items))}
	for _, item := range items {
		result.Items = append(result.Items, scheduleSummaryDTO(item))
	}
	return managementv1.ListSchedules200JSONResponse(result), nil
}

func scheduleSummaryDTO(item schedulecmd.Item) managementv1.ScheduleSummaryItem {
	result := managementv1.ScheduleSummaryItem{Definition: managementv1.ScheduleSummaryDefinition{ID: item.Definition.ID,
		Cron: item.Definition.Cron, Locator: item.Definition.Locator, Alias: item.Definition.Alias}, Source: item.Source,
		Enabled: item.Enabled, Deleted: item.Deleted, Version: item.Version, Status: item.Status}
	if !item.NextRunAt.IsZero() {
		next := item.NextRunAt.UTC()
		result.NextRunAt = &next
	}
	if !item.LastRunAt.IsZero() {
		last := item.LastRunAt.UTC()
		result.LastRunAt = &last
	}
	return result
}

func (s strictServer) GetSchedule(ctx context.Context, request managementv1.GetScheduleRequestObject) (managementv1.GetScheduleResponseObject, error) {
	item, err := s.schedules.Get(ctx, request.ScheduleID, scheduleAuthority(ctx))
	if err != nil {
		return nil, err
	}
	return managementv1.GetSchedule200JSONResponse(scheduleItemDTO(item)), nil
}

func (s strictServer) CreateSchedule(ctx context.Context, request managementv1.CreateScheduleRequestObject) (managementv1.CreateScheduleResponseObject, error) {
	if request.Body == nil {
		return nil, schedulecmd.ErrInvalid
	}
	item, err := s.schedules.Create(ctx, schedulecmd.Create{Definition: scheduleDefinition(request.Body.Definition), Authority: scheduleAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateSchedule200JSONResponse(scheduleItemDTO(item)), nil
}

func (s strictServer) UpdateSchedule(ctx context.Context, request managementv1.UpdateScheduleRequestObject) (managementv1.UpdateScheduleResponseObject, error) {
	if request.Body == nil || request.Body.Definition.ID != request.ScheduleID {
		return nil, schedulecmd.ErrInvalid
	}
	item, err := s.schedules.Update(ctx, schedulecmd.Update{ID: request.ScheduleID, ExpectedVersion: request.Body.ExpectedVersion,
		Definition: scheduleDefinition(request.Body.Definition), Authority: scheduleAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateSchedule200JSONResponse(scheduleItemDTO(item)), nil
}

func (s strictServer) SelectSchedule(ctx context.Context, request managementv1.SelectScheduleRequestObject) (managementv1.SelectScheduleResponseObject, error) {
	if request.Body == nil {
		return nil, schedulecmd.ErrInvalid
	}
	item, err := s.schedules.SetEnabled(ctx, schedulecmd.ChangeSelection{ID: request.ScheduleID,
		ExpectedVersion: request.Body.ExpectedVersion, Enabled: request.Body.Enabled, Authority: scheduleAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.SelectSchedule200JSONResponse(scheduleItemDTO(item)), nil
}

func (s strictServer) DeleteSchedule(ctx context.Context, request managementv1.DeleteScheduleRequestObject) (managementv1.DeleteScheduleResponseObject, error) {
	if request.Body == nil {
		return nil, schedulecmd.ErrInvalid
	}
	item, err := s.schedules.Delete(ctx, schedulecmd.Delete{ID: request.ScheduleID,
		ExpectedVersion: request.Body.ExpectedVersion, Authority: scheduleAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	return managementv1.DeleteSchedule200JSONResponse(scheduleItemDTO(item)), nil
}

func (s strictServer) RunScheduleNow(ctx context.Context, request managementv1.RunScheduleNowRequestObject) (managementv1.RunScheduleNowResponseObject, error) {
	if request.Body == nil {
		return nil, schedulecmd.ErrInvalid
	}
	run, err := s.schedules.RunNow(ctx, schedulecmd.RunNow{ID: request.ScheduleID, RequestKey: request.Body.RequestKey,
		ConfirmDisabled: request.Body.ConfirmDisabled, Authority: scheduleAuthority(ctx)})
	if err != nil {
		return nil, err
	}
	result, err := scheduleRunDTO(run)
	if err != nil {
		return nil, err
	}
	return managementv1.RunScheduleNow200JSONResponse(result), nil
}

func (s strictServer) ListScheduleRuns(ctx context.Context, request managementv1.ListScheduleRunsRequestObject) (managementv1.ListScheduleRunsResponseObject, error) {
	if (request.Params.BeforeAt == nil) != (request.Params.BeforeID == nil) {
		return nil, schedulecmd.ErrInvalid
	}
	var beforeAt time.Time
	var beforeID string
	if request.Params.BeforeAt != nil {
		beforeAt = request.Params.BeforeAt.UTC()
		beforeID = request.Params.BeforeID.String()
	}
	runs, err := s.schedules.History(ctx, request.ScheduleID, beforeAt, beforeID, scheduleRunPageSize+1, scheduleAuthority(ctx))
	if err != nil {
		return nil, err
	}
	result := managementv1.ScheduleRunPage{Items: make([]managementv1.ScheduleRunItem, 0, min(len(runs), scheduleRunPageSize))}
	for _, run := range runs[:min(len(runs), scheduleRunPageSize)] {
		mapped, mapErr := scheduleRunDTO(run)
		if mapErr != nil {
			return nil, mapErr
		}
		result.Items = append(result.Items, mapped)
	}
	if len(runs) > scheduleRunPageSize {
		last := runs[scheduleRunPageSize-1]
		id, parseErr := uuid.Parse(last.ID)
		if parseErr != nil {
			return nil, schedulecmd.ErrUnavailable
		}
		result.Next = &managementv1.ScheduleRunCursor{BeforeAt: last.RequestedAt.UTC(), BeforeID: id}
	}
	return managementv1.ListScheduleRuns200JSONResponse(result), nil
}

func (s strictServer) GetScheduleRun(ctx context.Context, request managementv1.GetScheduleRunRequestObject) (managementv1.GetScheduleRunResponseObject, error) {
	detail, err := s.schedules.RunDetail(ctx, request.ScheduleID, request.RunID.String(), scheduleAuthority(ctx))
	if err != nil {
		return nil, err
	}
	run, err := scheduleRunDTO(detail.Run)
	if err != nil {
		return nil, err
	}
	return managementv1.GetScheduleRun200JSONResponse(managementv1.ScheduleRunDetail{Run: run, Input: detail.Input, Output: detail.Output}), nil
}

func scheduleAuthority(ctx context.Context) schedulecmd.Authority {
	p := webAuthFromContext(ctx).principal
	return schedulecmd.Authority{UserID: p.User.ID, UserVersion: p.User.Version, CredentialVersion: p.User.Credential.Version,
		MFAVersion: p.MFAVersion, WebAuthSessionID: p.WebAuthSessionID, WebAuthSessionVersion: p.WebAuthSessionVersion, At: time.Now().UTC()}
}

func scheduleDefinition(definition managementv1.ScheduleDefinition) schedulecmd.Definition {
	return schedulecmd.Definition{ID: definition.ID, Cron: definition.Cron, Locator: definition.Locator, Alias: definition.Alias, Content: definition.Content}
}

func scheduleItemDTO(item schedulecmd.Item) managementv1.ScheduleItem {
	result := managementv1.ScheduleItem{Definition: managementv1.ScheduleDefinition{ID: item.Definition.ID, Cron: item.Definition.Cron,
		Locator: item.Definition.Locator, Alias: item.Definition.Alias, Content: item.Definition.Content}, Source: item.Source,
		Enabled: item.Enabled, Deleted: item.Deleted, Version: item.Version, Status: item.Status}
	if !item.NextRunAt.IsZero() {
		next := item.NextRunAt.UTC()
		result.NextRunAt = &next
	}
	if !item.LastRunAt.IsZero() {
		last := item.LastRunAt.UTC()
		result.LastRunAt = &last
	}
	return result
}

func scheduleRunDTO(run schedulecmd.RunItem) (managementv1.ScheduleRunItem, error) {
	id, err := uuid.Parse(run.ID)
	if err != nil {
		return managementv1.ScheduleRunItem{}, schedulecmd.ErrUnavailable
	}
	result := managementv1.ScheduleRunItem{ID: id, Trigger: run.Trigger, RequestedAt: run.RequestedAt.UTC(), State: run.State,
		SafeFailureCode: run.SafeFailureCode, ReportLocatorRef: run.ReportLocatorRef}
	if !run.DueAt.IsZero() {
		due := run.DueAt.UTC()
		result.DueAt = &due
	}
	if !run.CompletedAt.IsZero() {
		completed := run.CompletedAt.UTC()
		result.CompletedAt = &completed
	}
	return result, nil
}
