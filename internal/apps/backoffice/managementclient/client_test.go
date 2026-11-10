package managementclient

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestCompatibilityUsesGeneratedClientAndOperatorToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		require.Equal(t, "GET", request.Method)
		require.Equal(t, "/api/management/v1/compatibility", request.URL.Path)
		require.Equal(t, "Bearer operator-secret", request.Header.Get("Authorization"))
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"contract_id":%q,"contract_version":%q,"api_major":%d}`,
			managementv1.ContractID, managementv1.ContractVersion, managementv1.APIMajor)
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
	require.NoError(t, err)
	require.NoError(t, client.Compatibility(t.Context()))
}

func TestCompatibilityRejectsAuthenticationAndContractMismatch(t *testing.T) {
	for _, test := range []struct {
		name  string
		serve func(http.ResponseWriter)
		want  error
	}{
		{name: "unauthorized", serve: func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }, want: ErrUnauthorized},
		{name: "wrong id", serve: compatibilityBody("other.contract", managementv1.ContractVersion, managementv1.APIMajor), want: ErrIncompatible},
		{name: "old core version", serve: compatibilityBody(managementv1.ContractID, "1.0.0", managementv1.APIMajor), want: ErrIncompatible},
		{name: "newer version", serve: compatibilityBody(managementv1.ContractID, "2.0.0", managementv1.APIMajor), want: ErrIncompatible},
		{name: "wrong major", serve: compatibilityBody(managementv1.ContractID, managementv1.ContractVersion, 2), want: ErrIncompatible},
		{name: "malformed", serve: func(w http.ResponseWriter) { _, _ = w.Write([]byte("credential-canary")) }, want: ErrInternal},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) { test.serve(writer) }))
			defer server.Close()
			client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
			require.NoError(t, err)
			err = client.Compatibility(t.Context())
			require.ErrorIs(t, err, test.want)
			require.NotContains(t, err.Error(), "credential-canary")
			require.NotContains(t, err.Error(), "operator-secret")
		})
	}
}

func TestCompatibilityUnavailableIsBoundedAndRedacted(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBytes+128) + "credential-canary"))
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
	require.NoError(t, err)
	err = client.Compatibility(t.Context())
	require.Error(t, err)
	require.NotContains(t, err.Error(), "credential-canary")
	require.NotContains(t, err.Error(), "operator-secret")

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	require.ErrorIs(t, client.Compatibility(cancelled), ErrUnavailable)
}

func TestNewRejectsUnsafeConfiguration(t *testing.T) {
	for _, config := range []Config{
		{},
		{BaseURL: "https://127.0.0.1:8097", OperatorToken: "token"},
		{BaseURL: "http://127.0.0.1:8097/path", OperatorToken: "token"},
		{BaseURL: "http://127.0.0.1:8097", OperatorToken: ""},
		{BaseURL: "http://127.0.0.1:8097", OperatorToken: "token with spaces"},
		{BaseURL: "http://127.0.0.1:8097", OperatorToken: strings.Repeat("x", maxTokenBytes+1)},
	} {
		_, err := New(config)
		require.Error(t, err)
	}
	require.ErrorIs(t, (*Client)(nil).Compatibility(t.Context()), ErrUnavailable)
}

func compatibilityBody(contractID, version string, major int) func(http.ResponseWriter) {
	return func(writer http.ResponseWriter) {
		writer.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(writer, `{"contract_id":%q,"contract_version":%q,"api_major":%d}`, contractID, version, major)
	}
}
