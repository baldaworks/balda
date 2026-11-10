package browseredge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestWebhookSecretAppearsOnlyInMutationResponse(t *testing.T) {
	service := &webhookEdgeService{item: webhookEdgeItem(), secret: "one-response-secret"}
	app := newTestApp(t, service)
	create := mutationRequest("/balda/backoffice/webhooks", url.Values{"csrf_token": {"csrf"}, "name": {"orders"}, "prompt_template": {"{{.Body}}"}, "dedupe_source": {"request_id"}})
	create.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, create)
	require.Equal(t, http.StatusOK, created.Code)
	require.Contains(t, created.Body.String(), service.secret)
	require.Equal(t, "no-store", created.Header().Get("Cache-Control"))

	detail := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/webhooks/orders", nil)
	detail.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	read := httptest.NewRecorder()
	app.Handler().ServeHTTP(read, detail)
	require.Equal(t, http.StatusOK, read.Code)
	require.NotContains(t, read.Body.String(), service.secret)
}

func TestWebhookAmbiguousTestPreservesBodyAndRequestKeyWithoutRetry(t *testing.T) {
	service := &webhookEdgeService{item: webhookEdgeItem(), testErr: managementclient.ErrUnavailable}
	app := newTestApp(t, service)
	key := uuid.NewString()
	body := "binary\x00body"
	request := mutationRequest("/balda/backoffice/webhooks/orders/test", url.Values{"csrf_token": {"csrf"}, "expected_version": {"1"}, "request_key": {key}, "body": {body}})
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 1, service.testCalls)
	require.Equal(t, key, service.lastTest.RequestKey.String())
	require.Contains(t, response.Body.String(), key)
	require.Contains(t, response.Body.String(), "binary")
}

type webhookEdgeService struct {
	fakeService
	item      managementv1.WebhookItem
	secret    string
	testErr   error
	testCalls int
	lastTest  managementv1.WebhookTestRequest
}

func (s *webhookEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (s *webhookEdgeService) Webhooks(context.Context, managementclient.Auth) (managementv1.WebhookInventory, error) {
	return managementv1.WebhookInventory{Items: []managementv1.WebhookItem{s.item}}, nil
}
func (s *webhookEdgeService) Webhook(context.Context, managementclient.Auth, string) (managementv1.WebhookItem, error) {
	return s.item, nil
}
func (s *webhookEdgeService) CreateWebhook(context.Context, managementclient.Auth, managementv1.WebhookCreateRequest) (managementv1.WebhookSecretResult, error) {
	return managementv1.WebhookSecretResult{Item: s.item, Secret: s.secret}, nil
}
func (s *webhookEdgeService) UpdateWebhook(context.Context, managementclient.Auth, string, managementv1.WebhookUpdateRequest) (managementv1.WebhookItem, error) {
	return managementv1.WebhookItem{}, errors.New("unexpected call")
}
func (s *webhookEdgeService) SelectWebhook(context.Context, managementclient.Auth, string, managementv1.WebhookSelectionRequest) (managementv1.WebhookItem, error) {
	return managementv1.WebhookItem{}, errors.New("unexpected call")
}
func (s *webhookEdgeService) DeleteWebhook(context.Context, managementclient.Auth, string, managementv1.WebhookVersionRequest) (managementv1.WebhookItem, error) {
	return managementv1.WebhookItem{}, errors.New("unexpected call")
}
func (s *webhookEdgeService) RotateWebhook(context.Context, managementclient.Auth, string, managementv1.WebhookVersionRequest) (managementv1.WebhookSecretResult, error) {
	return managementv1.WebhookSecretResult{}, errors.New("unexpected call")
}
func (s *webhookEdgeService) TestWebhook(_ context.Context, _ managementclient.Auth, _ string, request managementv1.WebhookTestRequest) (managementv1.WebhookTestResult, error) {
	s.testCalls++
	s.lastTest = request
	return managementv1.WebhookTestResult{}, s.testErr
}
func (s *webhookEdgeService) WebhookHistory(context.Context, managementclient.Auth, string, *managementv1.ListWebhookHistoryParams) (managementv1.WebhookHistoryPage, error) {
	return managementv1.WebhookHistoryPage{}, nil
}
func (s *webhookEdgeService) WebhookHistoryDetail(context.Context, managementclient.Auth, string, string) (managementv1.WebhookHistoryDetail, error) {
	return managementv1.WebhookHistoryDetail{}, errors.New("unexpected call")
}

func webhookEdgeItem() managementv1.WebhookItem {
	now := time.Now().UTC()
	return managementv1.WebhookItem{Definition: managementv1.WebhookDefinition{Name: "orders", PromptTemplate: "{{.Body}}", DedupeSource: managementv1.WebhookDefinitionDedupeSourceRequestID}, Source: managementv1.WebhookItemSourceManaged, Enabled: true, Version: 1, AuthType: managementv1.WebhookItemAuthTypeHeader, CreatedAt: now, UpdatedAt: now}
}

var _ Service = (*webhookEdgeService)(nil)
var _ WebhookService = (*webhookEdgeService)(nil)
