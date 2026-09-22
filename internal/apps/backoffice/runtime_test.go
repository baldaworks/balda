package backoffice

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthHandlerIsNonSensitiveAndNoStore(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	healthHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
}
