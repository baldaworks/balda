package managementhttp

import (
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/systemviewcmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestSystemViewResponsesProjectAllowlistedSnapshot(t *testing.T) {
	server := strictServer{system: &systemviewcmd.Snapshot{
		Capabilities: []systemviewcmd.Capability{{ID: "telegram", Name: "Telegram", Mode: "webhook", RouteCount: 1,
			Settings:  []systemviewcmd.Setting{{Label: "Formatting", Value: "markdown"}},
			Endpoints: []systemviewcmd.Endpoint{{Label: "Webhook", URL: "/telegram"}}}},
		Agent: systemviewcmd.Agent{ProviderID: "primary", ProviderType: "openai", Profile: "default", Model: "gpt-test", ReasoningEffort: "high", GlobalInstruction: "safe"},
	}}

	overviewResponse, err := server.GetSystemOverview(t.Context(), managementv1.GetSystemOverviewRequestObject{})
	require.NoError(t, err)
	overview := overviewResponse.(managementv1.GetSystemOverview200JSONResponse)
	require.Equal(t, "telegram", overview.Capabilities[0].ID)
	require.Equal(t, "/telegram", overview.Capabilities[0].Endpoints[0].URL)

	agentResponse, err := server.GetSystemAgent(t.Context(), managementv1.GetSystemAgentRequestObject{})
	require.NoError(t, err)
	agent := agentResponse.(managementv1.GetSystemAgent200JSONResponse)
	require.Equal(t, "primary", agent.ProviderID)
	require.Equal(t, "safe", agent.GlobalInstruction)
}
