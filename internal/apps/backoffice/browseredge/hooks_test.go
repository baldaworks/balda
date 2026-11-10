package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestHookBrowserCreateConflictAndDeleteConfirmation(t *testing.T) {
	service := &hookEdgeService{inventory: managementv1.HookInventory{Version: 4, Definitions: []managementv1.HookDefinition{}}}
	app := newTestApp(t, service)
	create := mutationRequest("/balda/backoffice/hooks", url.Values{
		"name": {"prompt-check"}, "event": {string(managementv1.UserPromptSubmit)}, "command": {"/bin/check"},
		"args": {`["--json"]`}, "timeout_seconds": {"10"}, "enabled": {"yes"}, "expected_version": {"4"},
	})
	create.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, create)
	require.Equal(t, http.StatusSeeOther, created.Code)
	require.Equal(t, int64(4), service.created.ExpectedVersion)
	require.Equal(t, []string{"--json"}, service.created.Definition.Args)
	require.True(t, service.created.Definition.Enabled)

	service.updateErr = managementclient.ErrConflict
	update := mutationRequest("/balda/backoffice/hooks/prompt-check", url.Values{
		"event": {string(managementv1.Stop)}, "command": {"/bin/check"}, "args": {`[]`},
		"timeout_seconds": {"0"}, "expected_version": {"4"},
	})
	update.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	conflict := httptest.NewRecorder()
	app.Handler().ServeHTTP(conflict, update)
	require.Equal(t, http.StatusConflict, conflict.Code)
	require.Equal(t, "prompt-check", service.updated.Definition.Name)

	deleteRequest := mutationRequest("/balda/backoffice/hooks/prompt-check/delete", url.Values{"expected_version": {"4"}})
	deleteRequest.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	unconfirmed := httptest.NewRecorder()
	app.Handler().ServeHTTP(unconfirmed, deleteRequest)
	require.Equal(t, http.StatusBadRequest, unconfirmed.Code)
	require.Equal(t, 0, service.deleteCalls)

	confirmedRequest := mutationRequest("/balda/backoffice/hooks/prompt-check/delete", url.Values{"expected_version": {"4"}, "confirm": {"yes"}})
	confirmedRequest.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	confirmed := httptest.NewRecorder()
	app.Handler().ServeHTTP(confirmed, confirmedRequest)
	require.Equal(t, http.StatusSeeOther, confirmed.Code)
	require.Equal(t, 1, service.deleteCalls)
}

type hookEdgeService struct {
	fakeService
	inventory   managementv1.HookInventory
	created     managementv1.HookSaveRequest
	updated     managementv1.HookSaveRequest
	updateErr   error
	deleteCalls int
}

func (*hookEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *hookEdgeService) Hooks(context.Context, managementclient.Auth) (managementv1.HookInventory, error) {
	return s.inventory, nil
}
func (s *hookEdgeService) CreateHook(_ context.Context, _ managementclient.Auth, request managementv1.HookSaveRequest) error {
	s.created = request
	return nil
}
func (s *hookEdgeService) UpdateHook(_ context.Context, _ managementclient.Auth, _ string, request managementv1.HookSaveRequest) error {
	s.updated = request
	return s.updateErr
}
func (s *hookEdgeService) DeleteHook(context.Context, managementclient.Auth, string, managementv1.HookDeleteRequest) error {
	s.deleteCalls++
	return nil
}

var _ Service = (*hookEdgeService)(nil)
var _ HookService = (*hookEdgeService)(nil)
