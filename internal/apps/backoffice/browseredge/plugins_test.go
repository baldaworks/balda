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

func TestPluginBrowserUsesReviewedCandidateWithoutRetry(t *testing.T) {
	service := &pluginEdgeService{outcome: managementv1.PluginMutationOutcome{PluginName: "calendar", Accepted: true, RecoveryPending: true}}
	app := newTestApp(t, service)
	request := mutationRequest("/balda/backoffice/plugins/install", url.Values{
		"plugin_name": {"calendar"}, "marketplace_name": {"official"}, "candidate_token": {"candidate-token"}, "expected_state": {"plugin-state"},
	})
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.Equal(t, 1, service.mutateCalls)
	require.Equal(t, managementv1.PluginOperationInstall, service.mutation.Operation)
	require.Equal(t, "official", service.mutation.MarketplaceName)
	require.Equal(t, "candidate-token", service.mutation.CandidateToken)
	require.Equal(t, "plugin-state", service.mutation.ExpectedState)
}

func TestPluginBrowserRequiresMarketplaceRemovalConfirmation(t *testing.T) {
	service := &pluginEdgeService{}
	app := newTestApp(t, service)
	request := mutationRequest("/balda/backoffice/plugin-marketplaces/official/remove", url.Values{"expected_state": {"market-state"}})
	request.Header.Set("Cookie", accessCookie+"=access; "+csrfCookie+"=csrf")
	response := httptest.NewRecorder()
	app.Handler().ServeHTTP(response, request)

	require.Equal(t, http.StatusBadRequest, response.Code)
	require.Equal(t, 0, service.removeCalls)
}

type pluginEdgeService struct {
	fakeService
	mutation    managementv1.PluginMutationRequest
	outcome     managementv1.PluginMutationOutcome
	mutateCalls int
	removeCalls int
}

func (*pluginEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "admin", Role: managementv1.Administrator}, nil
}
func (*pluginEdgeService) Plugins(context.Context, managementclient.Auth) (managementv1.PluginInventory, error) {
	return managementv1.PluginInventory{Items: []managementv1.PluginInstalled{}}, nil
}
func (*pluginEdgeService) Plugin(context.Context, managementclient.Auth, string) (managementv1.PluginDetail, error) {
	return managementv1.PluginDetail{}, nil
}
func (*pluginEdgeService) ReviewPlugin(context.Context, managementclient.Auth, string, string) (managementv1.PluginCandidate, error) {
	return managementv1.PluginCandidate{}, nil
}
func (s *pluginEdgeService) MutatePlugin(_ context.Context, _ managementclient.Auth, _ string, request managementv1.PluginMutationRequest) (managementv1.PluginMutationOutcome, error) {
	s.mutateCalls++
	s.mutation = request
	return s.outcome, nil
}
func (*pluginEdgeService) PluginMarketplaces(context.Context, managementclient.Auth) (managementv1.PluginMarketplaceInventory, error) {
	return managementv1.PluginMarketplaceInventory{Items: []managementv1.PluginMarketplace{}}, nil
}
func (*pluginEdgeService) PluginMarketplace(context.Context, managementclient.Auth, string) (managementv1.PluginMarketplaceDetail, error) {
	return managementv1.PluginMarketplaceDetail{}, nil
}
func (*pluginEdgeService) AddPluginMarketplace(context.Context, managementclient.Auth, managementv1.PluginMarketplaceAddRequest) (managementv1.PluginMarketplaceDetail, error) {
	return managementv1.PluginMarketplaceDetail{}, nil
}
func (*pluginEdgeService) RefreshPluginMarketplace(context.Context, managementclient.Auth, string, managementv1.PluginMarketplaceMutationRequest) (managementv1.PluginMarketplaceDetail, error) {
	return managementv1.PluginMarketplaceDetail{}, nil
}
func (s *pluginEdgeService) RemovePluginMarketplace(context.Context, managementclient.Auth, string, managementv1.PluginMarketplaceMutationRequest) error {
	s.removeCalls++
	return nil
}

var _ Service = (*pluginEdgeService)(nil)
var _ PluginService = (*pluginEdgeService)(nil)
