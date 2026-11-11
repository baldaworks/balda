package backoffice

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type marketplaceInstalledFixture struct {
	pluginMarketplaceSecurityFixture
}

func (f *marketplaceInstalledFixture) Inventory(context.Context, plugincmd.Authority) ([]plugincmd.InstalledView, error) {
	return []plugincmd.InstalledView{{PluginStatus: plugincmd.PluginStatus{Name: "demo"}}}, f.readErr
}
func (f *marketplaceInstalledFixture) RefreshMarketplace(ctx context.Context, r plugincmd.MarketplaceMutation) (plugincmd.MarketplaceDetail, error) {
	f.refreshes = append(f.refreshes, r)
	detail, _ := f.Marketplace(ctx, r.Name, r.Authority)
	return detail, f.err
}

func TestPluginMarketplaceHTTPInstalledOnMutationError(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	now := time.Now().UTC()
	createAccessTestUser(t, provider.Users(), usercmd.User{ID: "admin", Username: "admin", NormalizedUsername: "admin", DisplayName: "admin", Role: usercmd.RoleAdministrator, Status: usercmd.StatusActive, Primary: true, Credential: usercmd.Credential{State: usercmd.CredentialStateActive, Version: 1}, Version: 1, CreatedAt: now, UpdatedAt: now})
	app, err := newHTTPApp(provider.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	fixture := &marketplaceInstalledFixture{pluginMarketplaceSecurityFixture: pluginMarketplaceSecurityFixture{err: plugincmd.ErrConflict}}
	app.plugins = fixture
	handler, err := app.handler()
	if err != nil {
		t.Fatal(err)
	}
	admin := loginHTTPAppSession(t, handler, config, "admin")
	for _, operation := range []string{"refresh", "remove"} {
		response := performAccessMutation(t, handler, config, "/plugins/marketplaces/definitions/new/"+operation, url.Values{"expected_state": {"current-state"}, "confirm": {checkedFormValue}, "csrf_token": {admin.csrf}}, admin.access, admin.csrf, true)
		if response.Code != http.StatusConflict || !strings.Contains(response.Body.String(), ">Yes</td>") || !strings.Contains(response.Body.String(), "Open installed") {
			t.Fatalf("%s lost installed state status=%d", operation, response.Code)
		}
	}
	before := len(fixture.refreshes)
	fixture.readErr = plugincmd.ErrUnavailable
	response := performAccessMutation(t, handler, config, "/plugins/marketplaces/definitions/new/refresh", url.Values{"expected_state": {"current-state"}, "csrf_token": {admin.csrf}}, admin.access, admin.csrf, false)
	if response.Code != http.StatusServiceUnavailable || len(fixture.refreshes) != before || strings.Contains(response.Body.String(), ">No</td>") {
		t.Fatal("failed installed read allowed mutation or invented status")
	}
}
