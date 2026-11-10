package managementclient

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSystemViewsUseOperatorAndWebAuthAccess(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		paths = append(paths, request.URL.Path)
		require.Equal(t, "Bearer operator-secret", request.Header.Get("Authorization"))
		require.Equal(t, "web-access", request.Header.Get("X-Balda-WebAuth-Access"))
		writer.Header().Set("Content-Type", "application/json")
		if request.URL.Path == "/api/management/v1/system/overview" {
			_, _ = writer.Write([]byte(`{"capabilities":[{"id":"telegram","name":"Telegram","mode":"polling","route_count":0,"settings":[],"endpoints":[]}]}`))
			return
		}
		_, _ = writer.Write([]byte(`{"provider_id":"primary","provider_type":"openai","profile":"default","model":"gpt-test","reasoning_effort":"high","global_instruction":"safe"}`))
	}))
	defer server.Close()

	client, err := New(Config{BaseURL: server.URL, OperatorToken: "operator-secret", HTTPClient: server.Client()})
	require.NoError(t, err)
	auth := Auth{Access: "web-access"}
	overview, err := client.SystemOverview(t.Context(), auth)
	require.NoError(t, err)
	require.Equal(t, "telegram", overview.Capabilities[0].ID)
	agent, err := client.SystemAgent(t.Context(), auth)
	require.NoError(t, err)
	require.Equal(t, "gpt-test", agent.Model)
	require.Equal(t, []string{"/api/management/v1/system/overview", "/api/management/v1/system/agent"}, paths)
}
