package managementhttp

import (
	"context"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func (s strictServer) GetSystemOverview(context.Context, managementv1.GetSystemOverviewRequestObject) (managementv1.GetSystemOverviewResponseObject, error) {
	capabilities := make([]managementv1.SystemCapability, 0, len(s.system.Capabilities))
	for _, source := range s.system.Capabilities {
		item := managementv1.SystemCapability{ID: source.ID, Name: source.Name, Mode: source.Mode, RouteCount: source.RouteCount,
			Settings: make([]managementv1.SystemSetting, 0, len(source.Settings)), Endpoints: make([]managementv1.SystemEndpoint, 0, len(source.Endpoints))}
		for _, setting := range source.Settings {
			item.Settings = append(item.Settings, managementv1.SystemSetting{Label: setting.Label, Value: setting.Value})
		}
		for _, endpoint := range source.Endpoints {
			item.Endpoints = append(item.Endpoints, managementv1.SystemEndpoint{Label: endpoint.Label, URL: endpoint.URL})
		}
		capabilities = append(capabilities, item)
	}
	return managementv1.GetSystemOverview200JSONResponse{Capabilities: capabilities}, nil
}

func (s strictServer) GetSystemAgent(context.Context, managementv1.GetSystemAgentRequestObject) (managementv1.GetSystemAgentResponseObject, error) {
	agent := s.system.Agent
	return managementv1.GetSystemAgent200JSONResponse{ProviderID: agent.ProviderID, ProviderType: agent.ProviderType, Profile: agent.Profile,
		Model: agent.Model, ReasoningEffort: agent.ReasoningEffort, GlobalInstruction: agent.GlobalInstruction}, nil
}
