package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestProviderPagesAndMutationsUseGeneratedClientSurface(t *testing.T) {
	service := &providerServiceFixture{fakeService: &fakeService{}, inventory: managementv1.ProviderInventory{Version: 4, Types: []string{"openai"}, Items: []managementv1.ProviderItem{
		{ID: "configured", Source: managementv1.ProviderSourceConfigured, CurrentRevisionID: "configured-revision", Enabled: true, DriverType: "openai", ContractVersion: 1, FormatVersion: 1, Config: managementv1.ProviderConfig{Type: "openai"}, HasAPIKey: true, EnvironmentNames: []string{"SECRET_NAME"}},
		{ID: "managed", Source: managementv1.ProviderSourceManaged, CurrentRevisionID: "managed-revision", Enabled: true, DriverType: "openai", ContractVersion: 1, FormatVersion: 1, Config: managementv1.ProviderConfig{Type: "openai"}},
	}}}
	app := newTestApp(t, service)

	list := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/providers", nil)
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	app.Handler().ServeHTTP(list, request)
	require.Equal(t, http.StatusOK, list.Code)
	require.Contains(t, list.Body.String(), "configured")
	require.NotContains(t, list.Body.String(), "provider-api-key-canary")

	configured := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "http://admin.test/balda/backoffice/providers/configured", nil)
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	app.Handler().ServeHTTP(configured, request)
	require.Equal(t, http.StatusOK, configured.Code)
	require.Contains(t, configured.Body.String(), "read-only")
	require.NotContains(t, configured.Body.String(), "method=\"post\"")

	create := mutationRequest("/balda/backoffice/providers", url.Values{
		"expected_version": {"4"}, "id": {"new-provider"}, "type": {"openai"}, "model": {"gpt-test"},
		"api_key_action": {"replace"}, "api_key": {"provider-api-key-canary"}, "environment_action": {"replace"}, "environment": {"TOKEN=environment-secret-canary,with-comma"},
	})
	created := httptest.NewRecorder()
	app.Handler().ServeHTTP(created, create)
	require.Equal(t, http.StatusSeeOther, created.Code)
	require.Equal(t, "/balda/backoffice/providers/new-provider", created.Header().Get("Location"))
	require.Equal(t, "new-provider", service.created.ID)
	require.Equal(t, "provider-api-key-canary", *service.created.Protected.APIKey.Value)
	require.Equal(t, "environment-secret-canary,with-comma", (*service.created.Protected.Environment.Values)["TOKEN"])
	require.NotContains(t, created.Body.String(), "provider-api-key-canary")

	selection := mutationRequest("/balda/backoffice/providers/managed/selection", url.Values{"expected_version": {"4"}, "confirm": {"yes"}})
	selected := httptest.NewRecorder()
	app.Handler().ServeHTTP(selected, selection)
	require.Equal(t, http.StatusSeeOther, selected.Code)
	require.False(t, service.selected.Enabled)

	service.mutationErr = managementclient.ErrConflict
	remove := mutationRequest("/balda/backoffice/providers/managed/delete", url.Values{"expected_version": {"4"}, "confirm": {"yes"}})
	deleted := httptest.NewRecorder()
	app.Handler().ServeHTTP(deleted, remove)
	require.Equal(t, http.StatusConflict, deleted.Code)
}

type providerServiceFixture struct {
	*fakeService
	inventory   managementv1.ProviderInventory
	created     managementv1.ProviderCreateRequest
	updated     managementv1.ProviderUpdateRequest
	selected    managementv1.ProviderSelectionRequest
	deleted     managementv1.ProviderDeleteRequest
	mutationErr error
}

func (f *providerServiceFixture) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Username: "admin", DisplayName: "Administrator", Role: managementv1.Administrator}, nil
}
func (f *providerServiceFixture) Providers(context.Context, managementclient.Auth) (managementv1.ProviderInventory, error) {
	return f.inventory, nil
}
func (f *providerServiceFixture) CreateProvider(_ context.Context, _ managementclient.Auth, request managementv1.ProviderCreateRequest) error {
	f.created = request
	return f.mutationErr
}
func (f *providerServiceFixture) UpdateProvider(_ context.Context, _ managementclient.Auth, _ string, request managementv1.ProviderUpdateRequest) error {
	f.updated = request
	return f.mutationErr
}
func (f *providerServiceFixture) SelectProvider(_ context.Context, _ managementclient.Auth, _ string, request managementv1.ProviderSelectionRequest) error {
	f.selected = request
	return f.mutationErr
}
func (f *providerServiceFixture) DeleteProvider(_ context.Context, _ managementclient.Auth, _ string, request managementv1.ProviderDeleteRequest) error {
	f.deleted = request
	return f.mutationErr
}

func TestProviderMutationRejectsBadOriginBeforeService(t *testing.T) {
	service := &providerServiceFixture{fakeService: &fakeService{}}
	app := newTestApp(t, service)
	request := mutationRequest("/balda/backoffice/providers", url.Values{"expected_version": {"0"}, "id": {"provider"}, "type": {"openai"}})
	request.Header.Set("Origin", "http://evil.test")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)
	require.Equal(t, http.StatusForbidden, response.Code)
	require.True(t, strings.TrimSpace(service.created.ID) == "")
}
