package hooknativehost

import (
	"context"
	"os"

	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

// Host is the provider-neutral capability surface used by concrete adapters.
type Host interface {
	Bind(ctx context.Context, scope hookprovider.BindingScope) (*hookprovider.Binding, error)
	Executable() string
}

// WriteMetadata persists private adapter-owned proxy metadata beside a binding.
func WriteMetadata(binding *hookprovider.Binding, adapter string) error {
	if err := os.WriteFile(binding.Reference()+".adapter", []byte(adapter), 0600); err != nil {
		return err
	}
	return os.WriteFile(binding.Reference()+".owner-proof", []byte(binding.OwnerProof()), 0600)
}

// WrapLaunch replaces one native leaf with the Balda proxy while preserving settings.
func WrapLaunch(config agentconfig.Config, executable, adapter string, binding *hookprovider.Binding, resolved agentconfig.ResolvedConfig) agentconfig.Config {
	config.Type = agentconfig.AgentTypeGenericACP
	config.GenericACP = &agentconfig.ACPConfig{
		Cmd:   append([]string{executable, "agent-hooks", "proxy", adapter, binding.Reference(), binding.OwnerProof(), "--"}, resolved.Command...),
		Model: resolved.Model, ModelConfigID: resolved.ModelConfigID,
		ReasoningEffort: resolved.ReasoningEffort, ReasoningEffortConfigID: resolved.ReasoningEffortConfigID,
		Mode: resolved.Mode,
	}
	config.ClaudeCodeACP, config.CodexACP, config.OpenCodeACP = nil, nil, nil
	return config
}
