package balda

import (
	"context"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/httpfx"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/webhookbackofficeapp"
	"github.com/baldaworks/balda/internal/apps/balda/webhookmanagement"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutefx"
)

// TestSharedHTTPLayoutBrowser checks rendered URLs and layout through the
// production shared-listener assembly before the separate application E2E gate.
func TestSharedHTTPLayoutBrowser(t *testing.T) {
	if os.Getenv("BALDA_BACKOFFICE_BROWSER_TEST") != "1" {
		t.Skip("enable BALDA_BACKOFFICE_BROWSER_TEST with Playwright installed")
	}
	for _, test := range []struct {
		name     string
		basePath string
	}{{name: "asus prefix", basePath: "/balda"}, {name: "root prefix"}} {
		t.Run(test.name, func(t *testing.T) {
			address := freeTestAddress(t)
			params := sharedHTTPTestParamsWithBasePath(t, address, test.basePath)
			callbackPath := test.basePath + "/webhooks/orders"
			manager := webhookmanagement.New(webhookroutefx.NewStore(params.StateProvider),
				webhookmanagement.ManagedPaths{Prefix: test.basePath + "/webhooks", Ownership: params.HTTPRegistry})
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
				"http://"+address, test.basePath)
			command.Dir = root
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("shared HTTP layout browser: %v\n%s", err, output)
			}
			for _, name := range []string{"orders", "mobile_orders"} {
				stored, found, err := params.StateProvider.WebhookRoutes().Get(t.Context(), name)
				if err != nil || !found || stored.Source != state.WebhookRouteSourceManaged ||
					stored.Path != test.basePath+"/webhooks/"+name {
					t.Fatalf("saved managed route %q = %+v, found=%t, error=%v", name, stored, found, err)
				}
			}
			t.Log(string(output))
		})
	}
}
