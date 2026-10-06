package backoffice

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/security"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const testBackofficeBasePath = "/balda"

func TestMFACeremonyUsesNativeSecretFreeScopedDocument(t *testing.T) {
	p, config := newHTTPAppTestState(t)
	config.Server.BasePath = testBackofficeBasePath
	app, err := newHTTPApp(p.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "http://backoffice.example/balda/login", strings.NewReader("csrf_token=test-csrf&password=never-reflect"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response := httptest.NewRecorder()
	app.renderMFACeremony(response, request, usercmd.MFALogin, security.CeremonyStart{Transaction: "opaque-transaction", Options: map[string]any{"publicKey": map[string]any{"challenge": "cHVibGlj"}}})
	body := response.Body.String()
	for _, want := range []string{`<!doctype html>`, `/balda/auth/webauthn/finish`, `hx-boost="false"`, `hx-history="false"`, `name="csrf_token" value="test-csrf"`, `JavaScript`, `data-webauthn`} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(body, "never-reflect") {
		t.Fatal("reflected submitted password")
	}
}

func TestMFAErrorsOfferScopedNativeRestartWithoutSessionRestorePromise(t *testing.T) {
	p, config := newHTTPAppTestState(t)
	config.Server.BasePath = testBackofficeBasePath
	app, err := newHTTPApp(p.Users(), config)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ route, link, message string }{
		{"/account/2fa/enable/start", "/balda/account", "current password"},
		{"/account/2fa/disable", "/balda/account", "current password"},
		{"/auth/webauthn/finish", "/balda/login", "failed or expired"},
	} {
		t.Run(tc.route, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "http://backoffice.example/balda"+tc.route, nil)
			w := httptest.NewRecorder()
			app.renderSecurityError(w, r, http.StatusUnauthorized)
			if w.Code != 401 {
				t.Fatalf("status=%d", w.Code)
			}
			body := w.Body.String()
			if !strings.Contains(body, `href="`+tc.link+`" hx-boost="false"`) || !strings.Contains(body, tc.message) || strings.Contains(body, "browser will check") {
				t.Fatalf("unactionable error: %s", body)
			}
		})
	}
}
