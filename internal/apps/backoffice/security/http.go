package security

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	// AccessCookieName is the host-only short-lived browser credential cookie.
	AccessCookieName = "balda_access"
	// RefreshCookieName is the path-scoped rotating refresh credential cookie.
	RefreshCookieName = "balda_refresh"
	// CSRFCookieName carries the session-bound CSRF proof used by HTML forms.
	CSRFCookieName = "balda_csrf"
	// RefreshPath is the only request path that receives the refresh cookie.
	RefreshPath = "/auth/session/refresh"

	defaultMaxBodyBytes = 8 << 10
)

var defaultReturnPrefixes = []string{"/", "/overview", "/access", "/account", "/audit"}

type browserService interface {
	Login(ctx context.Context, username string, password []byte) (Credentials, error)
	ValidateAccess(ctx context.Context, rawToken string) (Principal, error)
	ValidateCSRF(ctx context.Context, rawAccessToken, csrfToken string) error
	Refresh(ctx context.Context, rawToken, csrfToken string) (Credentials, error)
	Logout(ctx context.Context, rawAccessToken string) error
	ReplacePassword(ctx context.Context, rawAccessToken string, currentPassword, nextPassword []byte) (Credentials, error)
}

// HTTPConfig controls the browser-only trust boundary.
type HTTPConfig struct {
	TrustedOrigin         string
	SecureCookies         bool
	MaxBodyBytes          int64
	AllowedReturnPrefixes []string
}

// Browser implements the HTTP security boundary without owning page rendering.
type Browser struct {
	service       browserService
	trustedOrigin string
	secureCookies bool
	maxBodyBytes  int64
	returnPaths   []string
	random        io.Reader
}

// NewBrowser validates and creates the browser security HTTP boundary.
func NewBrowser(service browserService, config HTTPConfig) (*Browser, error) {
	if service == nil {
		return nil, fmt.Errorf("browser security service is required")
	}
	origin, err := url.Parse(strings.TrimSpace(config.TrustedOrigin))
	if err != nil || (origin.Scheme != "http" && origin.Scheme != "https") || origin.Host == "" || origin.User != nil || origin.RawQuery != "" || origin.Fragment != "" || (origin.Path != "" && origin.Path != "/") {
		return nil, fmt.Errorf("trusted origin must be an absolute origin")
	}
	if config.SecureCookies != (origin.Scheme == "https") {
		return nil, fmt.Errorf("cookie security must match the trusted origin scheme")
	}
	maxBodyBytes := config.MaxBodyBytes
	if maxBodyBytes == 0 {
		maxBodyBytes = defaultMaxBodyBytes
	}
	if maxBodyBytes < 256 || maxBodyBytes > 1<<20 {
		return nil, fmt.Errorf("browser form body bound is invalid")
	}
	returnPaths := append([]string(nil), config.AllowedReturnPrefixes...)
	if len(returnPaths) == 0 {
		returnPaths = append([]string(nil), defaultReturnPrefixes...)
	}
	for _, prefix := range returnPaths {
		if !validReturnPrefix(prefix) {
			return nil, fmt.Errorf("invalid return path prefix %q", prefix)
		}
	}
	return &Browser{
		service: service, trustedOrigin: strings.TrimSuffix(origin.String(), "/"),
		secureCookies: config.SecureCookies, maxBodyBytes: maxBodyBytes,
		returnPaths: returnPaths, random: rand.Reader,
	}, nil
}

// EnsureCSRF issues a host-only CSRF cookie when a page request has none.
// The returned value is suitable for a server-rendered hidden form field.
func (b *Browser) EnsureCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(CSRFCookieName); err == nil && cookie.Value != "" {
		return cookie.Value, nil
	}
	token, err := randomValue(b.random, csrfBytes)
	if err != nil {
		return "", fmt.Errorf("generate browser CSRF token: %w", err)
	}
	b.setCookie(w, CSRFCookieName, token, "/", time.Time{}, true)
	return token, nil
}

// CSRFToken returns the CSRF cookie for rendering an authenticated form.
func (b *Browser) CSRFToken(r *http.Request) string {
	cookie, err := r.Cookie(CSRFCookieName)
	if err != nil {
		return ""
	}
	return cookie.Value
}

// Login accepts only a guarded form POST and creates normal or restricted cookies.
func (b *Browser) Login(w http.ResponseWriter, r *http.Request) {
	form, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	password := []byte(form.Get("password"))
	defer zero(password)
	credentials, err := b.service.Login(r.Context(), form.Get("username"), password)
	if err != nil {
		b.writeServiceError(w, err)
		return
	}
	b.setCredentials(w, credentials)
	if credentials.Assurance == usercmd.SessionAssuranceRestricted {
		b.redirect(w, r, "", "/account/password")
		return
	}
	b.redirect(w, r, form.Get("return_to"), "/overview")
}

// Refresh consumes one refresh generation and never replays the original request.
func (b *Browser) Refresh(w http.ResponseWriter, r *http.Request) {
	form, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	refresh, err := r.Cookie(RefreshCookieName)
	if err != nil || refresh.Value == "" {
		b.clearCredentials(w)
		b.writeServiceError(w, ErrUnauthenticated)
		return
	}
	credentials, err := b.service.Refresh(r.Context(), refresh.Value, form.Get("csrf_token"))
	if err != nil {
		if errors.Is(err, ErrUnauthenticated) {
			b.clearCredentials(w)
		}
		b.writeServiceError(w, err)
		return
	}
	b.setCredentials(w, credentials)
	if credentials.Assurance == usercmd.SessionAssuranceRestricted {
		b.redirect(w, r, "", "/account/password")
		return
	}
	b.redirect(w, r, form.Get("return_to"), "/overview")
}

// Logout revokes the full family and clears all browser credentials.
func (b *Browser) Logout(w http.ResponseWriter, r *http.Request) {
	_, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	access, err := r.Cookie(AccessCookieName)
	if err != nil || access.Value == "" {
		b.clearCredentials(w)
		b.writeServiceError(w, ErrUnauthenticated)
		return
	}
	if err := b.service.ValidateCSRF(r.Context(), access.Value, b.CSRFToken(r)); err != nil {
		b.writeServiceError(w, err)
		return
	}
	if err := b.service.Logout(r.Context(), access.Value); err != nil && !errors.Is(err, ErrUnauthenticated) {
		b.writeServiceError(w, err)
		return
	}
	b.clearCredentials(w)
	b.redirect(w, r, "", "/login")
}

// ReplacePassword rotates the credential and installs a fresh normal family.
func (b *Browser) ReplacePassword(w http.ResponseWriter, r *http.Request) {
	form, ok := b.mutationForm(w, r)
	if !ok {
		return
	}
	access, err := r.Cookie(AccessCookieName)
	if err != nil || access.Value == "" {
		b.writeServiceError(w, ErrUnauthenticated)
		return
	}
	if err := b.service.ValidateCSRF(r.Context(), access.Value, b.CSRFToken(r)); err != nil {
		b.writeServiceError(w, err)
		return
	}
	currentPassword := []byte(form.Get("current_password"))
	nextPassword := []byte(form.Get("new_password"))
	defer zero(currentPassword)
	defer zero(nextPassword)
	credentials, err := b.service.ReplacePassword(r.Context(), access.Value, currentPassword, nextPassword)
	if err != nil {
		b.writeServiceError(w, err)
		return
	}
	b.setCredentials(w, credentials)
	b.redirect(w, r, "", "/account")
}

// Authenticate adds a current canonical principal to the request context.
func (b *Browser) Authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(AccessCookieName)
		if err != nil || cookie.Value == "" {
			b.writeServiceError(w, ErrUnauthenticated)
			return
		}
		principal, err := b.service.ValidateAccess(r.Context(), cookie.Value)
		if err != nil {
			b.writeServiceError(w, err)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), principalContextKey{}, principal)))
	})
}

// RequireNormal rejects restricted temporary-password sessions.
func (b *Browser) RequireNormal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			b.writeServiceError(w, ErrUnauthenticated)
			return
		}
		if principal.Assurance != usercmd.SessionAssuranceNormal {
			b.writeServiceError(w, ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// RequireAdministrator enforces the current canonical system role.
func (b *Browser) RequireAdministrator(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		principal, ok := PrincipalFromContext(r.Context())
		if !ok {
			b.writeServiceError(w, ErrUnauthenticated)
			return
		}
		if principal.Assurance != usercmd.SessionAssuranceNormal || principal.User.Role != usercmd.RoleAdministrator {
			b.writeServiceError(w, ErrForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// PrincipalFromContext returns a principal installed by Authenticate.
func PrincipalFromContext(ctx context.Context) (Principal, bool) {
	principal, ok := ctx.Value(principalContextKey{}).(Principal)
	return principal, ok
}

// SafeReturnPath reduces a caller-supplied redirect to an allowlisted local path.
func (b *Browser) SafeReturnPath(raw, fallback string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.IsAbs() || parsed.Host != "" || parsed.Path == "" || !strings.HasPrefix(parsed.Path, "/") || strings.HasPrefix(parsed.Path, "//") || strings.Contains(parsed.Path, `\`) {
		return fallback
	}
	cleaned := path.Clean(parsed.Path)
	for _, prefix := range b.returnPaths {
		if cleaned == prefix || (prefix != "/" && strings.HasPrefix(cleaned, prefix+"/")) {
			parsed.Path = cleaned
			parsed.RawPath = ""
			return parsed.RequestURI()
		}
	}
	return fallback
}

func (b *Browser) mutationForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return nil, false
	}
	if r.Header.Get("Origin") != b.trustedOrigin {
		http.Error(w, "request forbidden", http.StatusForbidden)
		return nil, false
	}
	site := strings.ToLower(strings.TrimSpace(r.Header.Get("Sec-Fetch-Site")))
	if site != "" && site != "same-origin" {
		http.Error(w, "request forbidden", http.StatusForbidden)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, b.maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return nil, false
	}
	csrfCookie, err := r.Cookie(CSRFCookieName)
	csrfForm := r.PostForm.Get("csrf_token")
	if err != nil || csrfCookie.Value == "" || csrfForm == "" || subtle.ConstantTimeCompare([]byte(csrfCookie.Value), []byte(csrfForm)) != 1 {
		http.Error(w, "request forbidden", http.StatusForbidden)
		return nil, false
	}
	return r.PostForm, true
}

func (b *Browser) setCredentials(w http.ResponseWriter, credentials Credentials) {
	b.setCookie(w, AccessCookieName, credentials.AccessToken, "/", credentials.AccessExpiresAt, true)
	b.setCookie(w, RefreshCookieName, credentials.RefreshToken, RefreshPath, credentials.RefreshExpiresAt, true)
	b.setCookie(w, CSRFCookieName, credentials.CSRFToken, "/", credentials.RefreshExpiresAt, true)
}

func (b *Browser) clearCredentials(w http.ResponseWriter) {
	b.clearCookie(w, AccessCookieName, "/")
	b.clearCookie(w, RefreshCookieName, RefreshPath)
	b.clearCookie(w, CSRFCookieName, "/")
}

func (b *Browser) setCookie(w http.ResponseWriter, name, value, cookiePath string, expires time.Time, httpOnly bool) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: cookiePath, Expires: expires,
		HttpOnly: httpOnly, Secure: b.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (b *Browser) clearCookie(w http.ResponseWriter, name, cookiePath string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Path: cookiePath, MaxAge: -1, Expires: time.Unix(1, 0).UTC(),
		HttpOnly: true, Secure: b.secureCookies, SameSite: http.SameSiteStrictMode,
	})
}

func (b *Browser) redirect(w http.ResponseWriter, r *http.Request, requested, fallback string) {
	http.Redirect(w, r, b.SafeReturnPath(requested, fallback), http.StatusSeeOther)
}

func (b *Browser) writeServiceError(w http.ResponseWriter, err error) {
	w.Header().Set("Cache-Control", "no-store")
	switch {
	case errors.Is(err, ErrUnauthenticated):
		http.Error(w, "authentication failed", http.StatusUnauthorized)
	case errors.Is(err, ErrForbidden):
		http.Error(w, "request forbidden", http.StatusForbidden)
	case errors.Is(err, usercmd.ErrInvalid):
		http.Error(w, "invalid request", http.StatusBadRequest)
	case errors.Is(err, usercmd.ErrConflict):
		http.Error(w, "request conflict", http.StatusConflict)
	default:
		http.Error(w, "internal server error", http.StatusInternalServerError)
	}
}

func validReturnPrefix(prefix string) bool {
	return prefix == path.Clean(prefix) && strings.HasPrefix(prefix, "/") && !strings.HasPrefix(prefix, "//") && !strings.Contains(prefix, `\`)
}

type principalContextKey struct{}
