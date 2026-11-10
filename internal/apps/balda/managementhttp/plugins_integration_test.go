package managementhttp

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientPluginLifecyclePreservesCorePolicy(t *testing.T) {
	fixture := &pluginFixture{recoveryPending: true}
	server := httptest.NewServer(newManagementTestHandler(t, nil, nil, nil, nil, nil, fixture, nil))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(operatorRequestEditor))
	if err != nil {
		t.Fatal(err)
	}
	credentials := generatedLogin(t, client)
	read := credentialRequestEditor(credentials.AccessToken, "")
	write := credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	inventory, err := client.ListPluginsWithResponse(t.Context(), read)
	if err != nil || inventory.JSON200 == nil || len(inventory.JSON200.Items) != 1 || fixture.inventoryCalls != 1 || fixture.candidateCalls != 0 || fixture.mutateCalls != 0 {
		t.Fatalf("inventory = %#v, calls = (%d,%d,%d), %v", inventory, fixture.inventoryCalls, fixture.candidateCalls, fixture.mutateCalls, err)
	}
	detail, err := client.GetPluginWithResponse(t.Context(), "calendar", read)
	if err != nil || detail.JSON200 == nil || detail.JSON200.Item.Name != "calendar" || fixture.detailCalls != 1 || fixture.candidateCalls != 0 {
		t.Fatalf("detail = %#v, calls = (%d,%d), %v", detail, fixture.detailCalls, fixture.candidateCalls, err)
	}
	candidate, err := client.ReviewPluginCandidateWithResponse(t.Context(), "calendar", &managementv1.ReviewPluginCandidateParams{}, read)
	if err != nil || candidate.JSON200 == nil || candidate.JSON200.CandidateToken != "candidate-token" || fixture.candidateCalls != 1 {
		t.Fatalf("candidate = %#v, calls = %d, %v", candidate, fixture.candidateCalls, err)
	}

	request := managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationUpgrade, CandidateToken: "candidate-token", ExpectedState: "plugin-state"}
	withoutCSRF, err := client.MutatePluginWithResponse(t.Context(), "calendar", request, read)
	if err != nil || withoutCSRF.StatusCode() != http.StatusForbidden || fixture.mutateCalls != 0 {
		t.Fatalf("mutation without csrf = %#v, calls = %d, %v", withoutCSRF, fixture.mutateCalls, err)
	}
	invalid := request
	invalid.CandidateToken = ""
	rejected, err := client.MutatePluginWithResponse(t.Context(), "calendar", invalid, write)
	if err != nil || rejected.StatusCode() != http.StatusBadRequest || fixture.mutateCalls != 0 {
		t.Fatalf("invalid mutation = %#v, calls = %d, %v", rejected, fixture.mutateCalls, err)
	}
	accepted, err := client.MutatePluginWithResponse(t.Context(), "calendar", request, write)
	if err != nil || accepted.JSON200 == nil || !accepted.JSON200.Accepted || !accepted.JSON200.RecoveryPending || fixture.mutateCalls != 1 {
		t.Fatalf("accepted mutation = %#v, calls = %d, %v", accepted, fixture.mutateCalls, err)
	}
	if fixture.authority.UserID != browserAuthTestUsername || fixture.authority.WebAuthSessionID == "" {
		t.Fatalf("authority = %#v", fixture.authority)
	}
	fixture.recoveryPending = false
	for _, test := range []struct {
		name    string
		request managementv1.PluginMutationRequest
	}{
		{name: "install", request: managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationInstall, MarketplaceName: "official", CandidateToken: "candidate", ExpectedState: "state"}},
		{name: "enable", request: managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationEnable, ExpectedState: "state"}},
		{name: "disable", request: managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationDisable, ExpectedState: "state", Confirm: true}},
		{name: "rollback", request: managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationRollback, Revision: "rev-1", ExpectedState: "state", Confirm: true}},
		{name: "remove", request: managementv1.PluginMutationRequest{Operation: managementv1.PluginOperationRemove, ExpectedState: "state", Confirm: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			response, callErr := client.MutatePluginWithResponse(t.Context(), "calendar", test.request, write)
			if callErr != nil || response.JSON200 == nil || !response.JSON200.Accepted || plugincmd.Operation(test.request.Operation) != fixture.mutation.Operation {
				t.Fatalf("response = %#v, mutation = %#v, %v", response, fixture.mutation, callErr)
			}
		})
	}
	credentials = generatedLogin(t, client)
	read = credentialRequestEditor(credentials.AccessToken, "")
	write = credentialRequestEditor(credentials.AccessToken, credentials.CSRFToken)

	marketplaces, err := client.ListPluginMarketplacesWithResponse(t.Context(), read)
	if err != nil || marketplaces.JSON200 == nil || len(marketplaces.JSON200.Items) != 1 || fixture.marketplaceReadCalls != 1 || fixture.marketplaceRefreshCalls != 0 {
		t.Fatalf("marketplaces = %#v, calls = (%d,%d), %v", marketplaces, fixture.marketplaceReadCalls, fixture.marketplaceRefreshCalls, err)
	}
	unconfirmed, err := client.RemovePluginMarketplaceWithResponse(t.Context(), "official", managementv1.PluginMarketplaceMutationRequest{ExpectedState: "market-state"}, write)
	if err != nil || unconfirmed.StatusCode() != http.StatusBadRequest || fixture.marketplaceRemoveCalls != 0 {
		t.Fatalf("unconfirmed remove = %#v, calls = %d, %v", unconfirmed, fixture.marketplaceRemoveCalls, err)
	}
	added, err := client.AddPluginMarketplaceWithResponse(t.Context(), managementv1.PluginMarketplaceAddRequest{Name: "extra", Source: "https://example.invalid/extra", Sparse: []string{}}, write)
	if err != nil || added.JSON200 == nil || added.JSON200.Item.Name != "extra" {
		t.Fatalf("add marketplace = %#v, %v", added, err)
	}
	refreshed, err := client.RefreshPluginMarketplaceWithResponse(t.Context(), "official", managementv1.PluginMarketplaceMutationRequest{ExpectedState: "market-state"}, write)
	if err != nil || refreshed.JSON200 == nil || fixture.marketplaceRefreshCalls != 1 {
		t.Fatalf("refresh marketplace = %#v, calls = %d, %v", refreshed, fixture.marketplaceRefreshCalls, err)
	}
	removed, err := client.RemovePluginMarketplaceWithResponse(t.Context(), "official", managementv1.PluginMarketplaceMutationRequest{ExpectedState: "market-state", Confirm: true}, write)
	if err != nil || removed.StatusCode() != http.StatusNoContent || fixture.marketplaceRemoveCalls != 1 {
		t.Fatalf("remove marketplace = %#v, calls = %d, %v", removed, fixture.marketplaceRemoveCalls, err)
	}
}

type pluginFixture struct {
	authority               plugincmd.Authority
	inventoryCalls          int
	detailCalls             int
	candidateCalls          int
	mutateCalls             int
	marketplaceReadCalls    int
	marketplaceRefreshCalls int
	marketplaceRemoveCalls  int
	mutation                plugincmd.PluginMutation
	recoveryPending         bool
}

func (f *pluginFixture) Inventory(_ context.Context, authority plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	f.inventoryCalls++
	f.authority = authority
	return []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "calendar", Version: "1.0.0", Revision: "rev-1"}, ExpectedState: "plugin-state"}}, nil
}

func (f *pluginFixture) Detail(_ context.Context, name string, authority plugincmd.Authority) (plugincmd.PluginDetail, error) {
	f.detailCalls++
	f.authority = authority
	return plugincmd.PluginDetail{InstalledView: plugincmd.InstalledView{PluginStatus: plugincmd.PluginStatus{Name: name, Version: "1.0.0", Revision: "rev-1"}, ExpectedState: "plugin-state"}}, nil
}

func (f *pluginFixture) Candidate(_ context.Context, name, marketplace string, authority plugincmd.Authority) (plugincmd.CandidateView, error) {
	f.candidateCalls++
	f.authority = authority
	return plugincmd.CandidateView{Name: name, Version: "1.1.0", Marketplace: marketplace, CandidateToken: "candidate-token", ExpectedState: "plugin-state"}, nil
}

func (f *pluginFixture) MutatePlugin(_ context.Context, request plugincmd.PluginMutation) (plugincmd.MutationOutcome, error) {
	f.mutateCalls++
	f.authority = request.Authority
	f.mutation = request
	if f.recoveryPending {
		return plugincmd.MutationOutcome{PluginName: request.PluginName, Accepted: true, RecoveryPending: true}, plugincmd.ErrRecoveryPending
	}
	return plugincmd.MutationOutcome{PluginName: request.PluginName, Accepted: true}, nil
}

func (f *pluginFixture) Marketplaces(_ context.Context, authority plugincmd.Authority) ([]plugincmd.MarketplaceView, error) {
	f.marketplaceReadCalls++
	f.authority = authority
	return []plugincmd.MarketplaceView{{Name: "official", Source: "https://example.invalid/plugins", ExpectedState: "market-state"}}, nil
}

func (f *pluginFixture) Marketplace(_ context.Context, name string, authority plugincmd.Authority) (plugincmd.MarketplaceDetail, error) {
	f.authority = authority
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: name, Source: "https://example.invalid/plugins", ExpectedState: "market-state"}}, nil
}

func (f *pluginFixture) AddMarketplace(_ context.Context, request plugincmd.MarketplaceAdd) (plugincmd.MarketplaceDetail, error) {
	f.authority = request.Authority
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: request.Name, Source: request.Source}}, nil
}

func (f *pluginFixture) RefreshMarketplace(_ context.Context, request plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error) {
	f.marketplaceRefreshCalls++
	f.authority = request.Authority
	return plugincmd.MarketplaceDetail{MarketplaceView: plugincmd.MarketplaceView{Name: request.Name}}, nil
}

func (f *pluginFixture) RemoveMarketplace(_ context.Context, request plugincmd.MarketplaceMutation) error {
	f.marketplaceRemoveCalls++
	f.authority = request.Authority
	if !request.Confirm {
		return errors.New("missing confirmation")
	}
	return nil
}
