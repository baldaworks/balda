package managementhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	webAuthAccessHeader  = "X-Balda-WebAuth-Access"
	webAuthRefreshHeader = "X-Balda-WebAuth-Refresh"
	csrfHeader           = "X-Balda-CSRF"
	maxCredentialBytes   = 4096
	maxManagementBody    = 140 << 10
)

type routeAuthority uint8

const (
	operatorOnly routeAuthority = iota
	webAuthRead
	webAuthMutation
	webAuthRefresh
)

type routeRule struct {
	authority routeAuthority
	body      bool
}

var managementRoutes = map[string]routeRule{
	http.MethodGet + " " + compatibilityPath:                        {authority: operatorOnly},
	http.MethodPost + " /api/management/v1/auth/login":              {authority: operatorOnly, body: true},
	http.MethodPost + " /api/management/v1/auth/login/mfa":          {authority: operatorOnly, body: true},
	http.MethodPost + " /api/management/v1/auth/refresh":            {authority: webAuthRefresh},
	http.MethodGet + " /api/management/v1/auth/me":                  {authority: webAuthRead},
	http.MethodPost + " /api/management/v1/auth/logout":             {authority: webAuthMutation},
	http.MethodPost + " /api/management/v1/auth/password":           {authority: webAuthMutation, body: true},
	http.MethodGet + " /api/management/v1/auth/mfa":                 {authority: webAuthRead},
	http.MethodDelete + " /api/management/v1/auth/mfa":              {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/enable":         {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/enable/finish":  {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/replace":        {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/replace/finish": {authority: webAuthMutation, body: true},
	http.MethodGet + " /api/management/v1/auth/sessions":            {authority: webAuthRead},
}

type webAuthContext struct {
	access    string
	refresh   string
	csrf      string
	principal browserauth.Principal
}

type webAuthContextKey struct{}

func managementRoute(method, path string) (routeRule, bool) {
	if rule, ok := managementRoutes[method+" "+path]; ok {
		return rule, true
	}
	const prefix = "/api/management/v1/auth/sessions/"
	const suffix = "/revoke"
	if method != http.MethodPost || !strings.HasPrefix(path, prefix) || !strings.HasSuffix(path, suffix) {
		return routeRule{}, false
	}
	id := strings.TrimSuffix(strings.TrimPrefix(path, prefix), suffix)
	if id == "" || strings.Contains(id, "/") {
		return routeRule{}, false
	}
	return routeRule{authority: webAuthMutation, body: true}, true
}

func (s strictServer) guard(rule routeRule, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rule.body {
			r.Body = http.MaxBytesReader(w, r.Body, maxManagementBody)
		}
		authority := webAuthContext{}
		var err error
		switch rule.authority {
		case operatorOnly:
		case webAuthRead, webAuthMutation:
			authority.access, err = requiredCredentialHeader(r, webAuthAccessHeader)
			if err == nil {
				authority.principal, err = s.browserAuth.ValidateAccess(r.Context(), authority.access)
			}
			if err == nil && rule.authority == webAuthMutation {
				authority.csrf, err = requiredCSRFHeader(r)
				if err == nil {
					err = s.browserAuth.ValidateCSRF(r.Context(), authority.access, authority.csrf)
				}
			}
		case webAuthRefresh:
			authority.refresh, err = requiredCredentialHeader(r, webAuthRefreshHeader)
			if err == nil {
				authority.csrf, err = requiredCSRFHeader(r)
			}
		}
		if err != nil {
			writeServiceError(w, r, err)
			return
		}
		ctx := context.WithValue(r.Context(), webAuthContextKey{}, authority)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requiredCredentialHeader(r *http.Request, name string) (string, error) {
	values := r.Header.Values(name)
	if len(values) != 1 || values[0] == "" || len(values[0]) > maxCredentialBytes {
		return "", browserauth.ErrUnauthenticated
	}
	return values[0], nil
}

func requiredCSRFHeader(r *http.Request) (string, error) {
	values := r.Header.Values(csrfHeader)
	if len(values) != 1 || values[0] == "" || len(values[0]) > maxCredentialBytes {
		return "", browserauth.ErrForbidden
	}
	return values[0], nil
}

func webAuthFromContext(ctx context.Context) webAuthContext {
	authority, _ := ctx.Value(webAuthContextKey{}).(webAuthContext)
	return authority
}

func writeServiceError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, browserauth.ErrUnauthenticated):
		writeError(w, r, http.StatusUnauthorized, "unauthorized", "unauthorized")
	case errors.Is(err, browserauth.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, browserauth.ErrRefreshConcurrent), errors.Is(err, usercmd.ErrConflict),
		errors.Is(err, usercmd.ErrCurrentSessionConfirmationRequired), errors.Is(err, usercmd.ErrLastAdministrator),
		errors.Is(err, usercmd.ErrPrimaryConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, usercmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, usercmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	default:
		writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
	}
}
