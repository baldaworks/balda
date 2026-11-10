package managementclient

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (c *Client) Schedules(ctx context.Context, auth Auth) (managementv1.ScheduleInventory, error) {
	response, err := c.generated.ListSchedulesWithResponse(ctx, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleInventory{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleInventory{}, responseError(response.StatusCode())
}

func (c *Client) Schedule(ctx context.Context, auth Auth, id string) (managementv1.ScheduleItem, error) {
	response, err := c.generated.GetScheduleWithResponse(ctx, id, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleItem{}, responseError(response.StatusCode())
}

func (c *Client) CreateSchedule(ctx context.Context, auth Auth, request managementv1.ScheduleCreateRequest) (managementv1.ScheduleItem, error) {
	response, err := c.generated.CreateScheduleWithResponse(ctx, request, authEditors(auth)...)
	return scheduleItemResponse(response, err)
}

func (c *Client) UpdateSchedule(ctx context.Context, auth Auth, id string, request managementv1.ScheduleUpdateRequest) (managementv1.ScheduleItem, error) {
	response, err := c.generated.UpdateScheduleWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleItem{}, responseError(response.StatusCode())
}

func (c *Client) SelectSchedule(ctx context.Context, auth Auth, id string, request managementv1.ScheduleSelectionRequest) (managementv1.ScheduleItem, error) {
	response, err := c.generated.SelectScheduleWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleItem{}, responseError(response.StatusCode())
}

func (c *Client) DeleteSchedule(ctx context.Context, auth Auth, id string, request managementv1.ScheduleDeleteRequest) (managementv1.ScheduleItem, error) {
	response, err := c.generated.DeleteScheduleWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleItem{}, responseError(response.StatusCode())
}

func (c *Client) RunSchedule(ctx context.Context, auth Auth, id string, request managementv1.ScheduleRunNowRequest) (managementv1.ScheduleRunItem, error) {
	response, err := c.generated.RunScheduleNowWithResponse(ctx, id, request, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleRunItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleRunItem{}, responseError(response.StatusCode())
}

func (c *Client) ScheduleRuns(ctx context.Context, auth Auth, id string, params *managementv1.ListScheduleRunsParams) (managementv1.ScheduleRunPage, error) {
	response, err := c.generated.ListScheduleRunsWithResponse(ctx, id, params, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleRunPage{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleRunPage{}, responseError(response.StatusCode())
}

func (c *Client) ScheduleRun(ctx context.Context, auth Auth, scheduleID, runID string) (managementv1.ScheduleRunDetail, error) {
	var parsedID managementv1.ScheduleRunID
	if err := parsedID.UnmarshalText([]byte(runID)); err != nil {
		return managementv1.ScheduleRunDetail{}, ErrInternal
	}
	response, err := c.generated.GetScheduleRunWithResponse(ctx, scheduleID, parsedID, authEditors(auth)...)
	if err != nil || response == nil {
		return managementv1.ScheduleRunDetail{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleRunDetail{}, responseError(response.StatusCode())
}

func scheduleItemResponse(response *managementv1.CreateScheduleResponse, err error) (managementv1.ScheduleItem, error) {
	if err != nil || response == nil {
		return managementv1.ScheduleItem{}, ErrUnavailable
	}
	if response.StatusCode() == http.StatusOK && response.JSON200 != nil {
		return *response.JSON200, nil
	}
	return managementv1.ScheduleItem{}, responseError(response.StatusCode())
}
