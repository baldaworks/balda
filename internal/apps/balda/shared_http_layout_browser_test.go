package balda

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice"
	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutefx"
)

// TestSharedHTTPLayoutBrowser checks rendered URLs and layout through the
// production shared-listener assembly before the separate application E2E gate.
func TestSharedHTTPLayoutBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, test := range []struct {
		name          string
		basePath      string
		missingOrigin bool
	}{
		{name: "asus prefix", basePath: "/balda"},
		{name: "root prefix"},
		{name: "asus prefix without public origin", basePath: "/balda", missingOrigin: true},
		{name: "root prefix without public origin", missingOrigin: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			address := freeTestAddress(t)
			params := sharedHTTPTestParamsWithBasePath(t, address, test.basePath)
			originMode := "public"
			if test.missingOrigin {
				originMode = "missing"
				params.Config.HTTP.BaseURL = ""
				params.Config.Backoffice.PublicURL = "http://" + address
				config, err := backofficeRuntimeConfig(params.Config, state.DatabaseConfig{})
				if err != nil {
					t.Fatal(err)
				}
				emptyOrigin := ""
				config.Server.WebhookPublicOrigin = &emptyOrigin
				params.Backoffice, err = backoffice.NewRuntime(config, params.StateProvider)
				if err != nil {
					t.Fatal(err)
				}
			}
			callbackPath := test.basePath + "/webhooks/orders"
			manager := webhookmanagement.New(webhookroutefx.NewStore(params.StateProvider))
			if err := manager.ReconcileConfig(t.Context(), []webhookroutecmd.ConfiguredRoute{{
				Name: "configured", PromptTemplate: "Configured: {{.RawBody}}", Enabled: true,
				AuthType: webhookroutecmd.AuthTypeHeader, AuthHeader: "X-Configured-Secret",
				DedupeSource: webhookroutecmd.DedupeSourceRequestID,
			}}); err != nil {
				t.Fatal(err)
			}
			if err := params.Backoffice.ConfigureWebhooksOperations(webhookbackofficeapp.New(
				manager, nil, params.StateProvider.WebhookAdmissions())); err != nil {
				t.Fatal(err)
			}
			callback := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusAccepted)
			})
			params.GatewayCallbacks = []httpfx.GatewayCallbackProvider{func(context.Context) ([]httpfx.GatewayCallback, error) {
				return []httpfx.GatewayCallback{
					{Owner: "slack events", Transport: "slack", Endpoint: "events", Handler: callback},
					{Owner: "slack commands", Transport: "slack", Endpoint: "commands", Handler: callback},
				}, nil
			}}
			params.WebhookHTTP = func(_ context.Context, registry *httpfx.Registry) error {
				if err := registry.SetWebhookLookup("generic webhooks", callback, func(_ context.Context, path string) (bool, error) {
					return path == callbackPath, nil
				}); err != nil {
					return err
				}
				return nil
			}
			server, err := startSharedHTTPIngress(t.Context(), params)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = server.Stop(context.Background()) })
			root, err := filepath.Abs("../../..")
			if err != nil {
				t.Fatal(err)
			}
			command := exec.CommandContext(t.Context(), "node", "qa/backoffice-e2e/shared-http-layout.cjs",
				"http://"+address, test.basePath, originMode)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("shared HTTP layout browser: %v\n%s", err, output)
			}
			for _, name := range []string{"orders", "mobile_orders"} {
				stored, found, err := params.StateProvider.WebhookRoutes().Get(t.Context(), name)
				if err != nil || !found || stored.Source != state.WebhookRouteSourceManaged ||
					stored.Name != name {
					t.Fatalf("saved managed route %q = %+v, found=%t, error=%v", name, stored, found, err)
				}
			}
			t.Log(string(output))
		})
	}
}
