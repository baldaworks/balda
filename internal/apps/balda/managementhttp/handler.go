package managementhttp

import (
	"net/http"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const compatibilityPath = "/api/management/v1/compatibility"

// Handler returns the authenticated management API handler.
func (c ResolvedConfig) Handler(service *browserauth.Service) http.Handler {
	server := strictServer{browserAuth: service}
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
		if !ok || (service == nil && r.URL.Path != compatibilityPath) {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		server.guard(rule, generated).ServeHTTP(w, r)
	})
	return withRequestID(c.auth.middleware(gated))
}
