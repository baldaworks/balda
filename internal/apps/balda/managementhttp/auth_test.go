package managementhttp

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestAuthenticatorMiddleware(t *testing.T) {
	const token = "operator-token_123"
	authenticator, err := newAuthenticator(token)
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name    string
		headers []string
		wantOK  bool
	}{
		{name: "missing"},
		{name: "valid", headers: []string{"Bearer " + token}, wantOK: true},
		{name: "wrong token", headers: []string{"Bearer different-token"}},
		{name: "wrong scheme", headers: []string{"Basic " + token}},
		{name: "lowercase scheme", headers: []string{"bearer " + token}},
		{name: "empty token", headers: []string{"Bearer "}},
		{name: "extra whitespace", headers: []string{"Bearer  " + token}},
		{name: "invalid token characters", headers: []string{"Bearer token:invalid"}},
		{name: "oversized token", headers: []string{"Bearer " + strings.Repeat("a", maxOperatorTokenBytes+1)}},
		{name: "duplicate", headers: []string{"Bearer " + token, "Bearer " + token}},
	} {
		t.Run(test.name, func(t *testing.T) {
			called := false
			handler := withRequestID(authenticator.middleware(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true
				w.WriteHeader(http.StatusNoContent)
			})))
			request := httptest.NewRequest(http.MethodGet, "/api/management/v1/compatibility", nil)
			for _, value := range test.headers {
				request.Header.Add(authorizationHeader, value)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)

			if called != test.wantOK {
				t.Fatalf("next handler called = %v, want %v", called, test.wantOK)
			}
			requestID := response.Header().Get(requestIDHeader)
			if requestID == "" || len(requestID) > 128 {
				t.Fatalf("request ID = %q, want bounded generated value", requestID)
			}
			if test.wantOK {
				if response.Code != http.StatusNoContent {
					t.Fatalf("status = %d, want %d", response.Code, http.StatusNoContent)
				}
				return
			}
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
			}
			var body managementv1.ManagementError
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "unauthorized" || body.Message != "unauthorized" || body.RequestID == nil || *body.RequestID != requestID {
				t.Fatalf("error body = %+v, request ID = %q", body, requestID)
			}
			if strings.Contains(response.Body.String(), token) {
				t.Fatal("unauthorized response exposed the operator token")
			}
		})
	}
}

func TestAuthenticatorRejectsBeforeReadingBody(t *testing.T) {
	authenticator, err := newAuthenticator("operator-token")
	if err != nil {
		t.Fatal(err)
	}
	body := &countingReader{Reader: strings.NewReader("{invalid")}
	request := httptest.NewRequest(http.MethodPost, "/api/management/v1/auth/login", io.NopCloser(body))
	request.Header.Set(authorizationHeader, "Bearer wrong-token")
	response := httptest.NewRecorder()
	handler := withRequestID(authenticator.middleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid authentication reached the next handler")
	})))

	handler.ServeHTTP(response, request)

	if body.reads != 0 {
		t.Fatalf("request body reads = %d, want 0", body.reads)
	}
}

func TestRequestIDIsAlwaysServerGenerated(t *testing.T) {
	const untrusted = "operator-token-canary"
	request := httptest.NewRequest(http.MethodGet, "/", nil)
	request.Header.Add(requestIDHeader, untrusted)
	request.Header.Add(requestIDHeader, "duplicate")
	response := httptest.NewRecorder()
	handler := withRequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := requestIDFromContext(r.Context()); got == "" || got == untrusted {
			t.Fatalf("context request ID = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))

	handler.ServeHTTP(response, request)

	if got := response.Header().Get(requestIDHeader); got == "" || got == untrusted || bytes.Contains(response.Body.Bytes(), []byte(untrusted)) {
		t.Fatalf("response request ID = %q", got)
	}
}

type countingReader struct {
	io.Reader
	reads int
}

func (r *countingReader) Read(p []byte) (int, error) {
	r.reads++
	return r.Reader.Read(p)
}
