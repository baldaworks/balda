package managementhttp

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/browserauth"
	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/schedulecmd"
	"github.com/baldaworks/balda/internal/apps/balda/skillcmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/webhookroutecmd"
)

const (
	webAuthAccessHeader  = "X-Balda-WebAuth-Access"
	webAuthRefreshHeader = "X-Balda-WebAuth-Refresh"
	csrfHeader           = "X-Balda-CSRF"
	maxCredentialBytes   = 4096
	maxManagementBody    = 4 << 20
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
	admin     bool
}

var managementRoutes = map[string]routeRule{
	http.MethodGet + " " + compatibilityPath:                                   {authority: operatorOnly},
	http.MethodPost + " /api/management/v1/auth/login":                         {authority: operatorOnly, body: true},
	http.MethodPost + " /api/management/v1/auth/login/mfa":                     {authority: operatorOnly, body: true},
	http.MethodPost + " /api/management/v1/auth/refresh":                       {authority: webAuthRefresh},
	http.MethodGet + " /api/management/v1/auth/me":                             {authority: webAuthRead},
	http.MethodPost + " /api/management/v1/auth/logout":                        {authority: webAuthMutation},
	http.MethodPost + " /api/management/v1/auth/password":                      {authority: webAuthMutation, body: true},
	http.MethodGet + " /api/management/v1/auth/mfa":                            {authority: webAuthRead},
	http.MethodDelete + " /api/management/v1/auth/mfa":                         {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/enable":                    {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/enable/finish":             {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/replace":                   {authority: webAuthMutation, body: true},
	http.MethodPost + " /api/management/v1/auth/mfa/replace/finish":            {authority: webAuthMutation, body: true},
	http.MethodGet + " /api/management/v1/auth/sessions":                       {authority: webAuthRead},
	http.MethodGet + " /api/management/v1/providers":                           {authority: webAuthRead, admin: true},
	http.MethodPost + " /api/management/v1/providers":                          {authority: webAuthMutation, body: true, admin: true},
	http.MethodGet + " /api/management/v1/mcp":                                 {authority: webAuthRead, admin: true},
	http.MethodPost + " /api/management/v1/mcp":                                {authority: webAuthMutation, body: true, admin: true},
	http.MethodPost + " /api/management/v1/mcp/probe":                          {authority: webAuthMutation, body: true, admin: true},
	http.MethodPost + " /api/management/v1/mcp/authorization/browser/onboard":  {authority: webAuthMutation, body: true, admin: true},
	http.MethodPost + " /api/management/v1/mcp/authorization/device/onboard":   {authority: webAuthMutation, body: true, admin: true},
	http.MethodPost + " /api/management/v1/mcp/authorization/browser/callback": {authority: webAuthRead, body: true, admin: true},
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
	if rule, ok := providerManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := mcpManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := scheduleManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := webhookManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := aliasManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := hookManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := pluginManagementRoute(method, path); ok {
		return rule, true
	}
	if rule, ok := skillManagementRoute(method, path); ok {
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

func skillManagementRoute(method, path string) (routeRule, bool) {
	const root = "/api/management/v1/skills"
	if path == root && method == http.MethodGet {
		return routeRule{authority: webAuthRead, admin: true}, true
	}
	for _, exact := range []string{root + "/reviews/install", root + "/reviews/accept", root + "/refresh", root + "/publication/retry"} {
		if path == exact && method == http.MethodPost {
			return routeRule{authority: webAuthMutation, body: exact != root+"/publication/retry", admin: true}, true
		}
	}
	if !strings.HasPrefix(path, root+"/") || method != http.MethodPost {
		return routeRule{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, root+"/"), "/")
	if len(parts) == 3 && parts[0] != "" && parts[1] == "reviews" && parts[2] == "update" {
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	}
	if len(parts) == 2 && parts[0] != "" && parts[1] == "lifecycle" {
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	}
	return routeRule{}, false
}

func pluginManagementRoute(method, path string) (routeRule, bool) {
	const plugins = "/api/management/v1/plugins"
	const marketplaces = "/api/management/v1/plugin-marketplaces"
	if path == plugins || path == marketplaces {
		if method == http.MethodGet {
			return routeRule{authority: webAuthRead, admin: true}, true
		}
		if path == marketplaces && method == http.MethodPost {
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
		return routeRule{}, false
	}
	if strings.HasPrefix(path, plugins+"/") {
		parts := strings.Split(strings.TrimPrefix(path, plugins+"/"), "/")
		switch {
		case len(parts) == 1 && parts[0] != "" && method == http.MethodGet:
			return routeRule{authority: webAuthRead, admin: true}, true
		case len(parts) == 2 && parts[0] != "" && parts[1] == "candidate" && method == http.MethodGet:
			return routeRule{authority: webAuthRead, admin: true}, true
		case len(parts) == 2 && parts[0] != "" && parts[1] == "lifecycle" && method == http.MethodPost:
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
		return routeRule{}, false
	}
	if strings.HasPrefix(path, marketplaces+"/") {
		parts := strings.Split(strings.TrimPrefix(path, marketplaces+"/"), "/")
		switch {
		case len(parts) == 1 && parts[0] != "" && method == http.MethodGet:
			return routeRule{authority: webAuthRead, admin: true}, true
		case len(parts) == 1 && parts[0] != "" && method == http.MethodDelete:
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		case len(parts) == 2 && parts[0] != "" && parts[1] == "refresh" && method == http.MethodPost:
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
	}
	return routeRule{}, false
}

func hookManagementRoute(method, path string) (routeRule, bool) {
	const root = "/api/management/v1/hooks"
	if path == root {
		switch method {
		case http.MethodGet:
			return routeRule{authority: webAuthRead, admin: true}, true
		case http.MethodPost:
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		default:
			return routeRule{}, false
		}
	}
	if !strings.HasPrefix(path, root+"/") {
		return routeRule{}, false
	}
	name := strings.TrimPrefix(path, root+"/")
	if name == "" || strings.Contains(name, "/") {
		return routeRule{}, false
	}
	if method == http.MethodPut || method == http.MethodDelete {
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	}
	return routeRule{}, false
}

func aliasManagementRoute(method, path string) (routeRule, bool) {
	const root = "/api/management/v1/aliases"
	if path == root {
		if method == http.MethodGet {
			return routeRule{authority: webAuthRead, admin: true}, true
		}
		if method == http.MethodPost {
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
		return routeRule{}, false
	}
	if !strings.HasPrefix(path, root+"/") {
		return routeRule{}, false
	}
	name := strings.TrimPrefix(path, root+"/")
	if name == "" || strings.Contains(name, "/") {
		return routeRule{}, false
	}
	if method == http.MethodGet {
		return routeRule{authority: webAuthRead, admin: true}, true
	}
	if method == http.MethodPut || method == http.MethodDelete {
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	}
	return routeRule{}, false
}

func webhookManagementRoute(method, path string) (routeRule, bool) {
	const root = "/api/management/v1/webhooks"
	if path == root {
		if method == http.MethodGet {
			return routeRule{authority: webAuthRead, admin: true}, true
		}
		if method == http.MethodPost {
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
		return routeRule{}, false
	}
	if !strings.HasPrefix(path, root+"/") {
		return routeRule{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, root+"/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return routeRule{}, false
	}
	switch {
	case len(parts) == 1 && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	case len(parts) == 1 && (method == http.MethodPut || method == http.MethodDelete):
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case len(parts) == 2 && (parts[1] == "selection" || parts[1] == "rotate" || parts[1] == "test") && method == http.MethodPost:
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case len(parts) == 2 && parts[1] == "history" && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	case len(parts) == 3 && parts[1] == "history" && parts[2] != "" && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	default:
		return routeRule{}, false
	}
}

func scheduleManagementRoute(method, path string) (routeRule, bool) {
	const root = "/api/management/v1/schedules"
	const runs = "runs"
	if path == root {
		switch method {
		case http.MethodGet:
			return routeRule{authority: webAuthRead, admin: true}, true
		case http.MethodPost:
			return routeRule{authority: webAuthMutation, body: true, admin: true}, true
		}
		return routeRule{}, false
	}
	if !strings.HasPrefix(path, root+"/") {
		return routeRule{}, false
	}
	parts := strings.Split(strings.TrimPrefix(path, root+"/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		return routeRule{}, false
	}
	switch {
	case len(parts) == 1 && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	case len(parts) == 1 && (method == http.MethodPut || method == http.MethodDelete):
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case len(parts) == 2 && parts[1] == "selection" && method == http.MethodPost:
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case len(parts) == 2 && parts[1] == runs && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	case len(parts) == 2 && parts[1] == runs && method == http.MethodPost:
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case len(parts) == 3 && parts[1] == runs && parts[2] != "" && method == http.MethodGet:
		return routeRule{authority: webAuthRead, admin: true}, true
	default:
		return routeRule{}, false
	}
}

func mcpManagementRoute(method, path string) (routeRule, bool) {
	const prefix = "/api/management/v1/mcp/"
	if !strings.HasPrefix(path, prefix) {
		return routeRule{}, false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if strings.HasPrefix(remainder, "authorization/device/") && method == http.MethodGet {
		id := strings.TrimPrefix(remainder, "authorization/device/")
		if id != "" && !strings.Contains(id, "/") {
			return routeRule{authority: webAuthRead, admin: true}, true
		}
		return routeRule{}, false
	}
	if strings.HasPrefix(remainder, "authorization/attempts/") && strings.HasSuffix(remainder, "/cancel") && method == http.MethodPost {
		id := strings.TrimSuffix(strings.TrimPrefix(remainder, "authorization/attempts/"), "/cancel")
		if id != "" && !strings.Contains(id, "/") {
			return routeRule{authority: webAuthMutation, admin: true}, true
		}
	}
	for _, suffix := range []string{"/authorization/browser", "/authorization/device", "/authorization/disconnect", "/authorization/retry"} {
		if strings.HasSuffix(remainder, suffix) {
			id := strings.TrimSuffix(remainder, suffix)
			if id != "" && !strings.Contains(id, "/") && method == http.MethodPost {
				return routeRule{authority: webAuthMutation, body: suffix != "/authorization/disconnect", admin: true}, true
			}
			return routeRule{}, false
		}
	}
	for _, suffix := range []string{"/probe", "/selection", "/diagnostic"} {
		if strings.HasSuffix(remainder, suffix) && strings.TrimSuffix(remainder, suffix) != "" && !strings.Contains(strings.TrimSuffix(remainder, suffix), "/") {
			if (suffix == "/probe" && method == http.MethodPut) || (suffix != "/probe" && method == http.MethodPost) {
				return routeRule{authority: webAuthMutation, body: suffix != "/diagnostic", admin: true}, true
			}
			return routeRule{}, false
		}
	}
	if remainder != "" && !strings.Contains(remainder, "/") && (method == http.MethodPut || method == http.MethodDelete) {
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	}
	return routeRule{}, false
}

func providerManagementRoute(method, path string) (routeRule, bool) {
	const prefix = "/api/management/v1/providers/"
	if !strings.HasPrefix(path, prefix) {
		return routeRule{}, false
	}
	remainder := strings.TrimPrefix(path, prefix)
	if remainder == "" || strings.Contains(strings.TrimSuffix(remainder, "/selection"), "/") {
		return routeRule{}, false
	}
	switch {
	case (method == http.MethodPut || method == http.MethodDelete) && !strings.HasSuffix(remainder, "/selection"):
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	case method == http.MethodPost && strings.HasSuffix(remainder, "/selection") && strings.TrimSuffix(remainder, "/selection") != "":
		return routeRule{authority: webAuthMutation, body: true, admin: true}, true
	default:
		return routeRule{}, false
	}
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
			if err == nil && rule.admin && authority.principal.User.Role != usercmd.RoleAdministrator {
				err = browserauth.ErrForbidden
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
	case errors.Is(err, browserauth.ErrRefreshConcurrent), errors.Is(err, providers.ErrConflict), errors.Is(err, usercmd.ErrConflict),
		errors.Is(err, usercmd.ErrCurrentSessionConfirmationRequired), errors.Is(err, usercmd.ErrLastAdministrator),
		errors.Is(err, usercmd.ErrPrimaryConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, providers.ErrInvalid), errors.Is(err, usercmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, mcpcmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, schedulecmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, schedulecmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, schedulecmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, schedulecmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, webhookroutecmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, webhookroutecmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, webhookroutecmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, webhookroutecmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, aliascmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, aliascmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, aliascmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, aliascmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, mcpcmd.ErrForbidden), errors.Is(err, mcpcmd.ErrAuthRequired):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, usercmd.ErrNotFound), errors.Is(err, mcpcmd.ErrNotFound), errors.Is(err, mcpcmd.ErrAuthAttempt):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, mcpcmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, providers.ErrUnavailable), errors.Is(err, mcpcmd.ErrCredentials), errors.Is(err, mcpcmd.ErrUnavailable), errors.Is(err, mcpcmd.ErrDisconnected):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, schedulecmd.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, webhookroutecmd.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, aliascmd.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, hookcmd.ErrInvalid), errors.Is(err, hookcmd.ErrUnsupported):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, hookcmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, hookcmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, hookcmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, hookcmd.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, plugincmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, plugincmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, plugincmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, plugincmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, plugincmd.ErrUnavailable), errors.Is(err, plugincmd.ErrRecoveryPending):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	case errors.Is(err, skillcmd.ErrInvalid):
		writeError(w, r, http.StatusBadRequest, "request.invalid", "invalid request")
	case errors.Is(err, skillcmd.ErrForbidden):
		writeError(w, r, http.StatusForbidden, "forbidden", "forbidden")
	case errors.Is(err, skillcmd.ErrNotFound):
		writeError(w, r, http.StatusNotFound, "not_found", "not found")
	case errors.Is(err, skillcmd.ErrConflict):
		writeError(w, r, http.StatusConflict, "conflict", "request conflict")
	case errors.Is(err, skillcmd.ErrUnavailable):
		writeError(w, r, http.StatusServiceUnavailable, "unavailable", "service unavailable")
	default:
		writeError(w, r, http.StatusInternalServerError, "internal", "internal error")
	}
}
