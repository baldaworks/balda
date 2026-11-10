package managementclient

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestMCPAuthorizationSideEffectIsNotRetried(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		require.Equal(t, http.MethodPost, request.Method)
		require.Equal(t, "/api/management/v1/mcp/managed/authorization/device", request.URL.Path)
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
	require.NoError(t, err)
	_, err = client.BeginMCPDevice(t.Context(), Auth{Access: "access", CSRF: "csrf"}, "managed", managementv1.MCPAuthorizationInput{})
	require.ErrorIs(t, err, ErrUnavailable)
	require.EqualValues(t, 1, requests.Load())
}

func TestMCPAuthorizationRejectsRedirectWithoutForwardingSecrets(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		targetRequests.Add(1)
	}))
	defer target.Close()
	var coreRequests atomic.Int32
	core := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		coreRequests.Add(1)
		body, err := io.ReadAll(request.Body)
		require.NoError(t, err)
		require.Contains(t, string(body), "secret-canary")
		writer.Header().Set("Location", target.URL)
		writer.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer core.Close()

	client, err := New(Config{BaseURL: core.URL, OperatorToken: "operator-secret", HTTPClient: core.Client()})
	require.NoError(t, err)
	secret := "secret-canary"
	_, err = client.BeginMCPBrowser(t.Context(), Auth{Access: "access-canary", CSRF: "csrf-canary"}, "managed", managementv1.MCPAuthorizationInput{ClientSecret: &secret})
	require.Error(t, err)
	require.EqualValues(t, 1, coreRequests.Load())
	require.Zero(t, targetRequests.Load())
}
