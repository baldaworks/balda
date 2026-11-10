package managementclient

import (
	"net/http"
	"net/http/httptest"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestMemoryMutationUsesGeneratedClientAuthorityWithoutRetry(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		require.Equal(t, http.MethodPut, request.Method)
		require.Equal(t, "/api/management/v1/memory/facts/7", request.URL.Path)
		require.Equal(t, "Bearer operator-secret", request.Header.Get("Authorization"))
		require.Equal(t, "access", request.Header.Get("X-Balda-WebAuth-Access"))
		require.Equal(t, "csrf", request.Header.Get("X-Balda-CSRF"))
		writer.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
	require.NoError(t, err)
	_, err = client.UpdateMemoryFact(t.Context(), Auth{Access: "access", CSRF: "csrf"}, 7, managementv1.MemoryFactUpdateRequest{Fact: "updated", ExpectedVersion: 3})
	require.ErrorIs(t, err, ErrUnavailable)
	require.Equal(t, 1, calls)
}
