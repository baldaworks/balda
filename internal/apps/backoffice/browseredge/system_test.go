package browseredge

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/backoffice/managementclient"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestSystemViewsEscapeConfiguredContent(t *testing.T) {
	service := &systemEdgeService{
		overview: managementv1.SystemOverview{Capabilities: []managementv1.SystemCapability{{ID: "telegram", Name: `<script>alert(1)</script>`, Mode: "polling", Settings: []managementv1.SystemSetting{}, Endpoints: []managementv1.SystemEndpoint{}}}},
		agent:    managementv1.SystemAgent{ProviderID: "primary", ProviderType: "openai", Profile: "default", Model: `<b>unsafe</b>`, ReasoningEffort: "high", GlobalInstruction: `<script>secret()</script>`},
	}
	app := newTestApp(t, service)
	for _, test := range []struct{ path, escaped string }{
		{path: "/balda/backoffice/overview", escaped: `&lt;script&gt;alert(1)&lt;/script&gt;`},
		{path: "/balda/backoffice/agent", escaped: `&lt;script&gt;secret()&lt;/script&gt;`},
	} {
		request := httptest.NewRequest(http.MethodGet, "http://admin.test"+test.path, nil)
		request.Header.Set("Cookie", accessCookie+"=access")
		response := httptest.NewRecorder()
		app.Handler().ServeHTTP(response, request)
		require.Equal(t, http.StatusOK, response.Code)
		require.Contains(t, response.Body.String(), test.escaped)
		require.NotContains(t, response.Body.String(), "<script>")
	}
}

type systemEdgeService struct {
	fakeService
	overview managementv1.SystemOverview
	agent    managementv1.SystemAgent
}

func (*systemEdgeService) Principal(context.Context, managementclient.Auth) (managementv1.WebAuthPrincipal, error) {
	return managementv1.WebAuthPrincipal{UserID: "operator", Role: managementv1.Administrator}, nil
}
func (s *systemEdgeService) SystemOverview(context.Context, managementclient.Auth) (managementv1.SystemOverview, error) {
	return s.overview, nil
}
func (s *systemEdgeService) SystemAgent(context.Context, managementclient.Auth) (managementv1.SystemAgent, error) {
	return s.agent, nil
}

var _ Service = (*systemEdgeService)(nil)
var _ SystemService = (*systemEdgeService)(nil)
