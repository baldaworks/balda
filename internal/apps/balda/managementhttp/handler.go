package managementhttp

import (
	"context"
	"net/http"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

const compatibilityPath = "/api/management/v1/compatibility"

// Handler returns the authenticated management API handler.
func (c ResolvedConfig) Handler() http.Handler {
	strict := managementv1.NewStrictHandler(strictServer{}, nil)
	mux := http.NewServeMux()
	generated := managementv1.HandlerFromMux(strict, mux)
	gated := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != compatibilityPath {
			writeError(w, r, http.StatusNotFound, "not_found", "not found")
			return
		}
		generated.ServeHTTP(w, r)
	})
	return withRequestID(c.auth.middleware(gated))
}

type strictServer struct{}

func (strictServer) GetManagementCompatibility(context.Context, managementv1.GetManagementCompatibilityRequestObject) (managementv1.GetManagementCompatibilityResponseObject, error) {
	return managementv1.GetManagementCompatibility200JSONResponse{
		ContractID:      managementv1.BaldaManagementV1,
		ContractVersion: managementv1.ContractVersion,
		APIMajor:        managementv1.N1,
	}, nil
}

func (strictServer) LoginWebAuth(context.Context, managementv1.LoginWebAuthRequestObject) (managementv1.LoginWebAuthResponseObject, error) {
	return nil, nil
}

func (strictServer) LogoutWebAuth(context.Context, managementv1.LogoutWebAuthRequestObject) (managementv1.LogoutWebAuthResponseObject, error) {
	return nil, nil
}

func (strictServer) GetWebAuthPrincipal(context.Context, managementv1.GetWebAuthPrincipalRequestObject) (managementv1.GetWebAuthPrincipalResponseObject, error) {
	return nil, nil
}

func (strictServer) RefreshWebAuth(context.Context, managementv1.RefreshWebAuthRequestObject) (managementv1.RefreshWebAuthResponseObject, error) {
	return nil, nil
}
