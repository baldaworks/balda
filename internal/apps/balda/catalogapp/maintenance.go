package catalogapp

import (
	"context"
	"os"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/agentconfig"
	"github.com/baldaworks/runtime/v2/mcpregistry"
)

// NewMaintenanceRuntime constructs the complete catalog compiler used by local
// maintenance commands without enabling process-local MCP or command
// projections. It does not start providers, transports, sessions, or servers.
func NewMaintenanceRuntime(stateDir string, provider state.Provider, configured map[string]agentconfig.MCPServerConfig) (*Runtime, error) {
	home, _ := os.UserHomeDir()
	runtime, err := NewRuntime(
		stateDir,
		agentSkillsDir(home),
		codexSkillsDir(home, os.Getenv("CODEX_HOME")),
		provider,
		nil,
		configured,
		mcpregistry.New(configured),
		commandcmd.NewRegistry(),
		nil,
		nil,
	)
	if err != nil {
		return nil, err
	}
	runtime.mcp = nil
	runtime.ads = nil
	runtime.maintenance = true
	return runtime, nil
}

// LoadMaintenanceSnapshot compiles current accepted sources into the in-memory
// read model without writing catalog or publication state.
func (r *Runtime) LoadMaintenanceSnapshot(ctx context.Context) error {
	selection, err := r.managedSkills.ReadSelections(ctx)
	if err != nil {
		return err
	}
	plugins, err := r.currentPluginSources(ctx)
	if err != nil {
		return err
	}
	candidate, err := r.prepareApplicationCandidateMode(ctx, plugins, nil, selection, false)
	if err != nil {
		return err
	}
	_, err = r.store.PublishApplication(candidate.Snapshot)
	return err
}
