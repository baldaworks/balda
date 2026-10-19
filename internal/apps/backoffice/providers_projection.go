package backoffice

import (
	"net/url"
	"slices"
	"strings"

	"github.com/baldaworks/balda/internal/apps/backoffice/internal/webui"
	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

func projectProviderRow(item providers.Item, version providers.StateVersion) webui.ProviderRow {
	row := webui.ProviderRow{ID: item.Record.ID, Source: string(item.Source), Type: item.Revision.DriverType,
		Revision: item.Revision.ID, Reproducible: item.Revision.Definition.Reproducible,
		ReadOnly: item.Source == providers.SourceConfigured, Deleted: item.Record.Deleted,
		Enabled: item.Record.Enabled, DetailPath: "/providers/" + url.PathEscape(item.Record.ID), Version: uint64(version)}
	resolved, err := agentconfig.NormalizeConfig(item.Revision.Definition.Config, "")
	if err == nil {
		row.ModelOrMode = resolved.Model
		if row.ModelOrMode == "" {
			row.ModelOrMode = resolved.Mode
		}
		row.Members = strings.Join(resolved.PoolMembers, ", ")
	}
	switch {
	case row.Deleted:
		row.State = "Deleted"
	case row.Enabled:
		row.State = "Enabled"
	default:
		row.State = "Disabled"
	}
	return row
}

func projectProviderEditor(item providers.Item, version providers.StateVersion, create bool) *webui.ProviderEditor {
	row := projectProviderRow(item, version)
	editor := &webui.ProviderEditor{Row: row, New: create, Action: row.DetailPath,
		HasAPIKey:        item.Revision.Definition.HasAPIKey,
		EnvironmentNames: append([]string(nil), item.Revision.Definition.EnvironmentNames...)}
	if create {
		editor.Action = "/providers"
		editor.Row.Version = uint64(version)
	}
	cfg := item.Revision.Definition.Config
	editor.SystemInstructions = cfg.SystemInstructions
	editor.MCPServers = strings.Join(cfg.MCPServers, "\n")
	if cfg.PoolConfig != nil {
		editor.PoolMembers = strings.Join(cfg.PoolConfig.Members, "\n")
	}
	if cfg.OpenAI != nil {
		editor.Model, editor.Timeout, editor.ReasoningEffort = cfg.OpenAI.Model, cfg.OpenAI.Timeout, cfg.OpenAI.ReasoningEffort
	}
	if cfg.AIStudio != nil {
		editor.Model, editor.Timeout, editor.ReasoningEffort = cfg.AIStudio.Model, cfg.AIStudio.Timeout, cfg.AIStudio.ReasoningEffort
	}
	if block := providerACPBlock(cfg); block != nil {
		editor.Model, editor.ReasoningEffort, editor.Mode = block.Model, block.ReasoningEffort, block.Mode
		editor.BridgeVersion, editor.RegistryID = block.BridgeVersion, block.RegistryID
		editor.ModelConfigID, editor.ReasoningEffortConfigID = block.ModelConfigID, block.ReasoningEffortConfigID
		editor.ExtraArgs = strings.Join(block.ExtraArgs, "\n")
		switch {
		case block.InheritEnv == nil:
			editor.InheritEnv = "default"
		case *block.InheritEnv:
			editor.InheritEnv = "yes"
		default:
			editor.InheritEnv = "no"
		}
	}
	selected := cfg.Type
	for _, descriptor := range agentconfig.BuiltinDriverRegistry().Descriptors() {
		if descriptor.Type == agentconfig.AgentTypeGenericACP || descriptor.Deprecated {
			continue
		}
		editor.Types = append(editor.Types, webui.ProviderTypeOption{Value: descriptor.Type, Label: descriptor.Type, Selected: descriptor.Type == selected})
	}
	if create && selected == "" && len(editor.Types) > 0 {
		editor.Types[0].Selected = true
	}
	slices.Sort(editor.EnvironmentNames)
	return editor
}

func providerACPBlock(cfg agentconfig.Config) *agentconfig.ACPConfig {
	switch cfg.Type {
	case agentconfig.AgentTypeGeminiACP:
		return cfg.GeminiACP
	case agentconfig.AgentTypeCodexACP:
		return cfg.CodexACP
	case agentconfig.AgentTypeOpenCodeACP:
		return cfg.OpenCodeACP
	case agentconfig.AgentTypeCopilotACP:
		return cfg.CopilotACP
	case agentconfig.AgentTypeClaudeCodeACP, agentconfig.AgentTypeClaudeACP:
		return cfg.ClaudeCodeACP
	case agentconfig.AgentTypeGrokACP:
		return cfg.GrokACP
	case agentconfig.AgentTypeRegistryACP:
		return cfg.RegistryACP
	case agentconfig.AgentTypeAgyACP:
		return cfg.AgyACP
	case agentconfig.AgentTypeAntigravityACP:
		return cfg.AntigravityACP
	default:
		return nil
	}
}
