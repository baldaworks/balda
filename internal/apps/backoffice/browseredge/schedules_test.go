package browseredge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestScheduleAmbiguousRunPreservesRequestKeyWithoutRetry(t *testing.T) {
	service := &scheduleEdgeService{}
	app := newTestApp(t, service)
	requestKey := uuid.NewString()
	request := mutationRequest("/balda/backoffice/schedules/daily/runs", url.Values{
		"csrf_token":       {"csrf"},
		"request_key":      {requestKey},
		"confirm_disabled": {"yes"},
	})
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	response := httptest.NewRecorder()

	app.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 1, service.runCalls)
	require.Equal(t, requestKey, service.lastRequest.RequestKey)
	require.Contains(t, response.Body.String(), requestKey)
	require.Contains(t, response.Body.String(), "may have been queued")
}

type scheduleEdgeService struct {
	fakeService
	runCalls    int
	lastRequest managementv1.ScheduleRunNowRequest
}

func (s *scheduleEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *scheduleEdgeService) Schedules(context.Context, managementclient.Auth) (managementv1.ScheduleInventory, error) {
	return managementv1.ScheduleInventory{}, nil
}
func (s *scheduleEdgeService) Schedule(context.Context, managementclient.Auth, string) (managementv1.ScheduleItem, error) {
	return managementv1.ScheduleItem{}, nil
}
func (s *scheduleEdgeService) CreateSchedule(context.Context, managementclient.Auth, managementv1.ScheduleCreateRequest) (managementv1.ScheduleItem, error) {
	return managementv1.ScheduleItem{}, errors.New("unexpected call")
}
func (s *scheduleEdgeService) UpdateSchedule(context.Context, managementclient.Auth, string, managementv1.ScheduleUpdateRequest) (managementv1.ScheduleItem, error) {
	return managementv1.ScheduleItem{}, errors.New("unexpected call")
}
func (s *scheduleEdgeService) SelectSchedule(context.Context, managementclient.Auth, string, managementv1.ScheduleSelectionRequest) (managementv1.ScheduleItem, error) {
	return managementv1.ScheduleItem{}, errors.New("unexpected call")
}
func (s *scheduleEdgeService) DeleteSchedule(context.Context, managementclient.Auth, string, managementv1.ScheduleDeleteRequest) (managementv1.ScheduleItem, error) {
	return managementv1.ScheduleItem{}, errors.New("unexpected call")
}
func (s *scheduleEdgeService) RunSchedule(_ context.Context, _ managementclient.Auth, _ string, request managementv1.ScheduleRunNowRequest) (managementv1.ScheduleRunItem, error) {
	s.runCalls++
	s.lastRequest = request
	return managementv1.ScheduleRunItem{}, managementclient.ErrUnavailable
}
func (s *scheduleEdgeService) ScheduleRuns(context.Context, managementclient.Auth, string, *managementv1.ListScheduleRunsParams) (managementv1.ScheduleRunPage, error) {
	return managementv1.ScheduleRunPage{}, nil
}
func (s *scheduleEdgeService) ScheduleRun(context.Context, managementclient.Auth, string, string) (managementv1.ScheduleRunDetail, error) {
	return managementv1.ScheduleRunDetail{}, errors.New("unexpected call")
}

var _ Service = (*scheduleEdgeService)(nil)
var _ ScheduleService = (*scheduleEdgeService)(nil)
