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

func TestMemoryBrowserLifecycleAndConflictPreservesInput(t *testing.T) {
	service := &memoryEdgeService{inventory: managementv1.MemoryFactInventory{Enabled: true, Version: 3, Facts: []managementv1.MemoryFact{{ID: 1, Fact: `<script>alpha</script>`, CreatedAt: "now"}}, TotalFacts: 1},
		editor: managementv1.MemoryFactEditor{Fact: managementv1.MemoryFact{ID: 1, Fact: "alpha", CreatedAt: "now"}, ExpectedVersion: 3}}
	app := newTestApp(t, service)

	list := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/memory", nil)
	list.Header.Set("Cookie", accessCookie+"=access")
	listed := httptest.NewRecorder()
	app.Handler().ServeHTTP(listed, list)
	require.Equal(t, http.StatusOK, listed.Code)
	require.Contains(t, listed.Body.String(), `&lt;script&gt;alpha&lt;/script&gt;`)
	require.NotContains(t, listed.Body.String(), `<script>`)

	service.updateErr = managementclient.ErrConflict
	update := mutationRequest("/balda/backoffice/memory/1", url.Values{"fact": {"unsaved value"}, "expected_version": {"3"}})
	update.Header.Add("Cookie", accessCookie+"=access")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, update)
	require.Equal(t, http.StatusConflict, response.Code)
	require.Contains(t, response.Body.String(), "unsaved value")
	require.Equal(t, int64(3), service.updated.ExpectedVersion)

	deleteRequest := mutationRequest("/balda/backoffice/memory/1/delete", url.Values{"expected_version": {"3"}})
	deleteRequest.Header.Add("Cookie", accessCookie+"=access")
	deleted := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleted, deleteRequest)
	require.Equal(t, http.StatusBadRequest, deleted.Code)
	require.Zero(t, service.deleteCalls)
}

type memoryEdgeService struct {
	fakeService
	inventory   managementv1.MemoryFactInventory
	editor      managementv1.MemoryFactEditor
	updated     managementv1.MemoryFactUpdateRequest
	updateErr   error
	deleteCalls int
}

func (*memoryEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *memoryEdgeService) MemoryFacts(context.Context, managementclient.Auth, string) (managementv1.MemoryFactInventory, error) {
	return s.inventory, nil
}
func (s *memoryEdgeService) MemoryFact(context.Context, managementclient.Auth, int64) (managementv1.MemoryFactEditor, error) {
	return s.editor, nil
}
func (*memoryEdgeService) CreateMemoryFact(context.Context, managementclient.Auth, managementv1.MemoryFactCreateRequest) (managementv1.MemoryFact, error) {
	return managementv1.MemoryFact{}, nil
}
func (s *memoryEdgeService) UpdateMemoryFact(_ context.Context, _ managementclient.Auth, _ int64, request managementv1.MemoryFactUpdateRequest) (managementv1.MemoryFact, error) {
	s.updated = request
	return managementv1.MemoryFact{}, s.updateErr
}
func (s *memoryEdgeService) DeleteMemoryFact(context.Context, managementclient.Auth, int64, managementv1.MemoryFactDeleteRequest) error {
	s.deleteCalls++
	return nil
}

var _ Service = (*memoryEdgeService)(nil)
var _ MemoryService = (*memoryEdgeService)(nil)
