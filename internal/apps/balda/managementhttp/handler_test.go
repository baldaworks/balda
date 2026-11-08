package managementhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestGeneratedClientCompatibility(t *testing.T) {
	server := httptest.NewServer(testHandler(t))
	t.Cleanup(server.Close)
	client, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set(authorizationHeader, "Bearer secret")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}

	response, err := client.GetManagementCompatibilityWithResponse(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode() != http.StatusOK || response.JSON200 == nil {
		t.Fatalf("status = %d, body = %s", response.StatusCode(), response.Body)
	}
	if response.JSON200.ContractID != managementv1.BaldaManagementV1 || response.JSON200.ContractVersion != managementv1.ContractVersion {
		t.Fatalf("compatibility = %#v", response.JSON200)
	}

	invalidClient, err := managementv1.NewClientWithResponses(server.URL, managementv1.WithRequestEditorFn(func(_ context.Context, request *http.Request) error {
		request.Header.Set(authorizationHeader, "Bearer wrong")
		return nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	unauthorized, err := invalidClient.GetManagementCompatibilityWithResponse(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if unauthorized.StatusCode() != http.StatusUnauthorized || unauthorized.JSON401 == nil {
		t.Fatalf("invalid token status = %d, body = %s", unauthorized.StatusCode(), unauthorized.Body)
	}
}

func TestHandlerServesAuthenticatedCompatibility(t *testing.T) {
	handler := testHandler(t)
	request := httptest.NewRequest(http.MethodGet, compatibilityPath, nil)
	request.Header.Set(authorizationHeader, "Bearer secret")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var compatibility managementv1.ManagementCompatibility
	if err := json.Unmarshal(response.Body.Bytes(), &compatibility); err != nil {
		t.Fatal(err)
	}
	if compatibility.ContractID != managementv1.BaldaManagementV1 || compatibility.APIMajor != managementv1.N1 {
		t.Fatalf("compatibility = %#v", compatibility)
	}
}

func TestHandlerRejectsInactiveRoutesBeforeGeneratedDecoder(t *testing.T) {
	handler := testHandler(t)
	for _, test := range []struct {
		name, method, path, contentType string
	}{
		{"deferred malformed body", http.MethodPost, "/api/management/v1/auth/login", "application/json"},
		{"deferred wrong method", http.MethodGet, "/api/management/v1/auth/login", ""},
		{"compatibility wrong method", http.MethodPost, compatibilityPath, "application/json"},
		{"unknown", http.MethodGet, "/api/management/v1/unknown", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, bytes.NewBufferString("{"))
			request.Header.Set(authorizationHeader, "Bearer secret")
			request.Header.Set("Content-Type", test.contentType)
			response := httptest.NewRecorder()

			handler.ServeHTTP(response, request)

			if response.Code != http.StatusNotFound {
				t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
			}
			var body managementv1.ManagementError
			if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Code != "not_found" || body.Message != "not found" {
				t.Fatalf("error = %#v", body)
			}
		})
	}
}

func TestHandlerAuthenticatesBeforeRouteGate(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/api/management/v1/auth/login", bytes.NewBufferString("{"))
	response := httptest.NewRecorder()

	testHandler(t).ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
}

func testHandler(t *testing.T) http.Handler {
	t.Helper()
	config, err := (Config{Enabled: true, ListenAddr: "127.0.0.1:8081", OperatorToken: "secret"}).Resolve()
	if err != nil {
		t.Fatal(err)
	}
	return config.Handler(nil)
}
