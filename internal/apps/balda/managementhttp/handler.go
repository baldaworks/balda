package managementhttp

import (
	"context"
	"net/http"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const compatibilityPath = "/api/management/v1/compatibility"

// Services are the Core-owned capabilities exposed through management v1.
type Services struct {
	BrowserAuth *browserauth.Service
	Providers   *providers.Service
	MCP         mcpOperations
}

type mcpOperations interface {
	Inventory(ctx context.Context) ([]mcpcmd.Item, error)
	ProviderIDs(ctx context.Context) ([]string, error)
	Create(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	Update(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
	SetEnabled(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Delete(ctx context.Context, request mcpcmd.ChangeSelection) (mcpcmd.Item, error)
	Probe(ctx context.Context, request mcpcmd.CreateDefinition) (mcpcmd.Item, error)
	ProbeUpdate(ctx context.Context, request mcpcmd.UpdateDefinition) (mcpcmd.Item, error)
	DiagnoseCurrent(ctx context.Context, id string, authority mcpcmd.Authority) (mcpcmd.DiagnosticResult, error)
	CreateAndBeginBrowser(ctx context.Context, creation mcpcmd.CreateDefinition, authorization mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.BrowserAuthorization, error)
	CreateAndBeginDevice(ctx context.Context, creation mcpcmd.CreateDefinition, authorization mcpcmd.BeginAuthorization) (mcpcmd.Item, mcpcmd.DeviceAuthorization, error)
	BeginBrowser(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.BrowserAuthorization, error)
	BeginDevice(ctx context.Context, request mcpcmd.BeginAuthorization) (mcpcmd.DeviceAuthorization, error)
	CompleteBrowser(ctx context.Context, callback mcpcmd.BrowserCallback) (mcpcmd.Item, error)
	Device(ctx context.Context, attemptID string, authority mcpcmd.Authority) (mcpcmd.DeviceAuthorization, error)
	CurrentAttempt(ctx context.Context, connectionID string, authority mcpcmd.Authority) (mcpcmd.AuthorizationAttempt, bool, error)
	Cancel(ctx context.Context, attemptID string, authority mcpcmd.Authority) error
	Disconnect(ctx context.Context, connectionID string, authority mcpcmd.Authority) error
	RetryAuthorization(ctx context.Context, request mcpcmd.SelectAuthorization) (mcpcmd.Item, error)
}

// Handler returns the authenticated management API handler.
func (c ResolvedConfig) Handler(services Services) http.Handler {
	server := strictServer{browserAuth: services.BrowserAuth, providers: services.Providers, mcp: services.MCP}
	strict := managementv1.NewStrictHandlerWithOptions(server, nil, managementv1.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, _ error) {
			writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
		},
		ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			writeServiceError(w, r, err)
		},
	})
	mux := http.NewServeMux()
	generated := managementv1.HandlerFromMux(strict, mux)
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rule, ok := managementRoute(r.Method, r.URL.Path)
		if !ok || (services.BrowserAuth == nil && r.URL.Path != compatibilityPath) {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		server.guard(rule, generated).ServeHTTP(w, r)
	})
	return withRequestID(c.auth.middleware(gated))
}
