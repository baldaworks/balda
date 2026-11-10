package managementhttp

import (
	"context"
	"slices"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/google/uuid"
)

func (s strictServer) ListProviders(ctx context.Context, _ managementv1.ListProvidersRequestObject) (managementv1.ListProvidersResponseObject, error) {
	if s.providers == nil {
		return nil, providers.ErrUnavailable
	}
	inventory, err := s.providers.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	return managementv1.ListProviders200JSONResponse(providerInventoryDTO(inventory)), nil
}

func (s strictServer) CreateProvider(ctx context.Context, request managementv1.CreateProviderRequestObject) (managementv1.CreateProviderResponseObject, error) {
	if s.providers == nil {
		return nil, providers.ErrUnavailable
	}
	if request.Body == nil || request.Body.ID == "" {
		return nil, providers.ErrInvalid
	}
	config, err := providerConfig(request.Body.Config)
	if err != nil {
		return nil, err
	}
	protected, err := providerProtectedEdit(request.Body.Protected, nil)
	if err != nil {
		return nil, err
	}
	_, err = s.providers.Create(ctx, providers.Change{ExpectedVersion: providers.StateVersion(request.Body.ExpectedVersion), ID: request.Body.ID,
		Config: config, Protected: protected, Audit: providerAudit(webAuthFromContext(ctx))})
	if err != nil {
		return nil, err
	}
	return managementv1.CreateProvider204Response{}, nil
}

func (s strictServer) UpdateProvider(ctx context.Context, request managementv1.UpdateProviderRequestObject) (managementv1.UpdateProviderResponseObject, error) {
	if s.providers == nil || request.Body == nil || request.ProviderID == "" {
		return nil, providers.ErrInvalid
	}
	environmentNames, err := s.managedProviderEnvironmentNames(ctx, request.ProviderID)
	if err != nil {
		return nil, err
	}
	config, err := providerConfig(request.Body.Config)
	if err != nil {
		return nil, err
	}
	protected, err := providerProtectedEdit(request.Body.Protected, environmentNames)
	if err != nil {
		return nil, err
	}
	_, err = s.providers.Update(ctx, providers.Change{ExpectedVersion: providers.StateVersion(request.Body.ExpectedVersion), ID: request.ProviderID,
		Config: config, Protected: protected, Audit: providerAudit(webAuthFromContext(ctx))})
	if err != nil {
		return nil, err
	}
	return managementv1.UpdateProvider204Response{}, nil
}

func (s strictServer) SelectProvider(ctx context.Context, request managementv1.SelectProviderRequestObject) (managementv1.SelectProviderResponseObject, error) {
	if s.providers == nil || request.Body == nil || request.ProviderID == "" {
		return nil, providers.ErrInvalid
	}
	if _, err := s.managedProviderEnvironmentNames(ctx, request.ProviderID); err != nil {
		return nil, err
	}
	_, err := s.providers.SetEnabled(ctx, providers.StateVersion(request.Body.ExpectedVersion), request.ProviderID, request.Body.Enabled, providerAudit(webAuthFromContext(ctx)))
	if err != nil {
		return nil, err
	}
	return managementv1.SelectProvider204Response{}, nil
}

func (s strictServer) DeleteProvider(ctx context.Context, request managementv1.DeleteProviderRequestObject) (managementv1.DeleteProviderResponseObject, error) {
	if s.providers == nil || request.Body == nil || request.ProviderID == "" {
		return nil, providers.ErrInvalid
	}
	if _, err := s.managedProviderEnvironmentNames(ctx, request.ProviderID); err != nil {
		return nil, err
	}
	_, err := s.providers.Delete(ctx, providers.StateVersion(request.Body.ExpectedVersion), request.ProviderID, providerAudit(webAuthFromContext(ctx)))
	if err != nil {
		return nil, err
	}
	return managementv1.DeleteProvider204Response{}, nil
}

func (s strictServer) managedProviderEnvironmentNames(ctx context.Context, providerID string) ([]string, error) {
	inventory, err := s.providers.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	for _, item := range inventory.Items {
		if item.Record.ID != providerID {
			continue
		}
		if item.Source != providers.SourceManaged || item.Record.Deleted {
			return nil, providers.ErrInvalid
		}
		return item.Revision.Definition.EnvironmentNames, nil
	}
	return nil, providers.ErrUnavailable
}

func providerInventoryDTO(inventory providers.Inventory) managementv1.ProviderInventory {
	result := managementv1.ProviderInventory{Version: uint64(inventory.Version), Types: providerTypes(), Items: make([]managementv1.ProviderItem, 0, len(inventory.Items))}
	for _, item := range inventory.Items {
		result.Items = append(result.Items, managementv1.ProviderItem{
			ID: item.Record.ID, Source: managementv1.ProviderSource(item.Source), CurrentRevisionID: item.Record.CurrentRevisionID,
			Enabled: item.Record.Enabled, Deleted: item.Record.Deleted, DriverType: item.Revision.DriverType,
			ContractVersion: item.Revision.ContractVersion, FormatVersion: item.Revision.FormatVersion,
			Config: providerConfigDTO(item.Revision.Definition.Config), HasAPIKey: item.Revision.Definition.HasAPIKey,
			EnvironmentNames: slices.Clone(item.Revision.Definition.EnvironmentNames), Reproducible: item.Revision.Definition.Reproducible,
		})
	}
	return result
}

func providerTypes() []string {
	result := make([]string, 0)
	for _, descriptor := range agentconfig.BuiltinDriverRegistry().Descriptors() {
		if descriptor.Type == agentconfig.AgentTypeGenericACP || descriptor.Deprecated {
			continue
		}
		result = append(result, descriptor.Type)
	}
	return result
}

func providerConfigDTO(config agentconfig.Config) managementv1.ProviderConfig {
	result := managementv1.ProviderConfig{Type: config.Type}
	setString := func(value string) *string {
		if value == "" {
			return nil
		}
		copyValue := value
		return &copyValue
	}
	if len(config.MCPServers) > 0 {
		value := slices.Clone(config.MCPServers)
		result.McpServers = &value
	}
	result.SystemInstructions = setString(config.SystemInstructions)
	if config.PoolConfig != nil {
		value := slices.Clone(config.PoolConfig.Members)
		result.PoolMembers = &value
	}
	if block := providerLocalAPIBlock(config); block != nil {
		result.Model, result.Timeout, result.ReasoningEffort = setString(block.Model), setString(block.Timeout), setString(block.ReasoningEffort)
	}
	if block := providerACPBlock(config); block != nil {
		result.Model, result.ModelConfigID = setString(block.Model), setString(block.ModelConfigID)
		result.ReasoningEffort, result.ReasoningEffortConfigID = setString(block.ReasoningEffort), setString(block.ReasoningEffortConfigID)
		result.Mode, result.BridgeVersion, result.RegistryID = setString(block.Mode), setString(block.BridgeVersion), setString(block.RegistryID)
		result.InheritEnv = block.InheritEnv
		if len(block.ExtraArgs) > 0 {
			value := slices.Clone(block.ExtraArgs)
			result.ExtraArgs = &value
		}
	}
	return result
}

func providerConfig(input managementv1.ProviderConfig) (agentconfig.Config, error) {
	config := agentconfig.Config{Type: input.Type, MCPServers: cloneStrings(input.McpServers), SystemInstructions: stringValue(input.SystemInstructions)}
	descriptor, ok := agentconfig.BuiltinDriverRegistry().Lookup(input.Type)
	if !ok || descriptor.Deprecated || descriptor.Type == agentconfig.AgentTypeGenericACP {
		return agentconfig.Config{}, providers.ErrInvalid
	}
	switch descriptor.Class {
	case agentconfig.DriverClassHosted:
		block := &agentconfig.LocalAPIConfig{Model: stringValue(input.Model), Timeout: stringValue(input.Timeout), ReasoningEffort: stringValue(input.ReasoningEffort)}
		switch descriptor.Type {
		case agentconfig.AgentTypeOpenAI:
			config.OpenAI = block
		case agentconfig.AgentTypeAIStudio:
			config.AIStudio = block
		default:
			return agentconfig.Config{}, providers.ErrInvalid
		}
	case agentconfig.DriverClassPool:
		config.PoolConfig = &agentconfig.PoolConfig{Members: cloneStrings(input.PoolMembers)}
	case agentconfig.DriverClassACP:
		block := &agentconfig.ACPConfig{ExtraArgs: cloneStrings(input.ExtraArgs), Model: stringValue(input.Model), ModelConfigID: stringValue(input.ModelConfigID),
			ReasoningEffort: stringValue(input.ReasoningEffort), ReasoningEffortConfigID: stringValue(input.ReasoningEffortConfigID),
			Mode: stringValue(input.Mode), BridgeVersion: stringValue(input.BridgeVersion), RegistryID: stringValue(input.RegistryID), InheritEnv: input.InheritEnv}
		if !setProviderACPBlock(&config, descriptor.Type, block) {
			return agentconfig.Config{}, providers.ErrInvalid
		}
	default:
		return agentconfig.Config{}, providers.ErrInvalid
	}
	return config, nil
}

func providerProtectedEdit(input managementv1.ProviderProtectedEdit, currentEnvironment []string) (providers.ProtectedEdit, error) {
	result := providers.ProtectedEdit{}
	switch input.APIKey.Operation {
	case managementv1.ProviderEditOperationKeep:
		if input.APIKey.Value != nil {
			return result, providers.ErrInvalid
		}
	case managementv1.ProviderEditOperationReplace:
		if input.APIKey.Value == nil {
			return result, providers.ErrInvalid
		}
		value := *input.APIKey.Value
		result.APIKey = &value
	case managementv1.ProviderEditOperationClear:
		if input.APIKey.Value != nil {
			return result, providers.ErrInvalid
		}
		value := ""
		result.APIKey = &value
	default:
		return result, providers.ErrInvalid
	}
	switch input.Environment.Operation {
	case managementv1.ProviderEditOperationKeep:
		if input.Environment.Values != nil {
			return result, providers.ErrInvalid
		}
	case managementv1.ProviderEditOperationReplace, managementv1.ProviderEditOperationClear:
		if input.Environment.Operation == managementv1.ProviderEditOperationClear && input.Environment.Values != nil ||
			input.Environment.Operation == managementv1.ProviderEditOperationReplace && input.Environment.Values == nil {
			return result, providers.ErrInvalid
		}
		result.Environment = make(map[string]*string, len(currentEnvironment))
		for _, name := range currentEnvironment {
			result.Environment[name] = nil
		}
		if input.Environment.Values != nil {
			for name, value := range *input.Environment.Values {
				copyValue := value
				result.Environment[name] = &copyValue
			}
		}
	default:
		return result, providers.ErrInvalid
	}
	return result, nil
}

func providerAudit(authority webAuthContext) usercmd.AuditEvent {
	return usercmd.AuditEvent{ID: uuid.NewString(), ActorUserID: authority.principal.User.ID,
		ActorWebAuthSessionID: authority.principal.WebAuthSessionID, Source: "management-api", OccurredAt: time.Now().UTC()}
}

func cloneStrings(value *[]string) []string {
	if value == nil {
		return nil
	}
	return slices.Clone(*value)
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func providerLocalAPIBlock(config agentconfig.Config) *agentconfig.LocalAPIConfig {
	if config.OpenAI != nil {
		return config.OpenAI
	}
	return config.AIStudio
}

func providerACPBlock(config agentconfig.Config) *agentconfig.ACPConfig {
	switch config.Type {
	case agentconfig.AgentTypeGeminiACP:
		return config.GeminiACP
	case agentconfig.AgentTypeCodexACP:
		return config.CodexACP
	case agentconfig.AgentTypeOpenCodeACP:
		return config.OpenCodeACP
	case agentconfig.AgentTypeCopilotACP:
		return config.CopilotACP
	case agentconfig.AgentTypeClaudeCodeACP, agentconfig.AgentTypeClaudeACP:
		return config.ClaudeCodeACP
	case agentconfig.AgentTypeGrokACP:
		return config.GrokACP
	case agentconfig.AgentTypeRegistryACP:
		return config.RegistryACP
	case agentconfig.AgentTypeAgyACP:
		return config.AgyACP
	case agentconfig.AgentTypeAntigravityACP:
		return config.AntigravityACP
	default:
		return nil
	}
}

func setProviderACPBlock(config *agentconfig.Config, providerType string, block *agentconfig.ACPConfig) bool {
	switch providerType {
	case agentconfig.AgentTypeGeminiACP:
		config.GeminiACP = block
	case agentconfig.AgentTypeCodexACP:
		config.CodexACP = block
	case agentconfig.AgentTypeOpenCodeACP:
		config.OpenCodeACP = block
	case agentconfig.AgentTypeCopilotACP:
		config.CopilotACP = block
	case agentconfig.AgentTypeClaudeCodeACP:
		config.ClaudeCodeACP = block
	case agentconfig.AgentTypeGrokACP:
		config.GrokACP = block
	case agentconfig.AgentTypeRegistryACP:
		config.RegistryACP = block
	case agentconfig.AgentTypeAgyACP:
		config.AgyACP = block
	case agentconfig.AgentTypeAntigravityACP:
		config.AntigravityACP = block
	default:
		return false
	}
	return true
}
