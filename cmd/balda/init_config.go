package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/normahq/balda/internal/git"
	"gopkg.in/yaml.v3"
)

func ensureBaldaConfigGitignore(configDir string) error {
	gitignorePath := filepath.Join(configDir, ".gitignore")
	if _, err := os.Stat(gitignorePath); err == nil {
		return nil
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat %s: %w", gitignorePath, err)
	}
	if err := os.WriteFile(gitignorePath, []byte(baldaConfigGitignoreContent), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", gitignorePath, err)
	}
	return nil
}

func buildBaldaInitDocument(workingDir string) (map[string]any, []string, error) {
	detectedAgents := detectBaldaInitAgents()
	if len(detectedAgents) == 0 {
		return nil, nil, fmt.Errorf(
			"no supported agent CLI detected in PATH; install at least one of: codex, opencode, copilot, gemini, claude",
		)
	}

	var baldaDefaults map[string]any
	if err := yaml.Unmarshal(defaultBaldaConfig, &baldaDefaults); err != nil {
		return nil, nil, fmt.Errorf("parse default balda config template: %w", err)
	}

	baldaSection, ok := toStringAnyMap(baldaDefaults["balda"])
	if !ok {
		return nil, nil, fmt.Errorf("default balda template is missing balda section")
	}
	ensureBaldaMCPServersDefault(baldaSection)
	baldaBaseBranch, err := baldaInitCurrentBranch(workingDir)
	if err != nil {
		baldaBaseBranch = ""
	}
	if err := setBaldaWorkspaceBaseBranch(baldaSection, baldaBaseBranch); err != nil {
		return nil, nil, err
	}

	agentIDs := make([]string, 0, len(detectedAgents))
	for _, detected := range detectedAgents {
		agentIDs = append(agentIDs, detected.ID)
	}

	doc := map[string]any{
		"runtime": map[string]any{
			"providers":   buildBaldaInitAgents(detectedAgents),
			"mcp_servers": map[string]any{},
		},
		"balda":    baldaSection,
		"profiles": buildBaldaInitProfiles(agentIDs),
	}

	return doc, agentIDs, nil
}

func detectBaldaInitAgents() []baldaInitAgentTemplate {
	detected := make([]baldaInitAgentTemplate, 0, len(baldaInitAgentTemplates))
	for _, template := range baldaInitAgentTemplates {
		for _, binary := range template.DetectBinary {
			if _, err := baldaInitLookPath(binary); err == nil {
				detected = append(detected, template)
				break
			}
		}
	}
	return detected
}

func buildBaldaInitAgents(detected []baldaInitAgentTemplate) map[string]any {
	agents := make(map[string]any, len(detected)+1)
	poolMembers := make([]any, 0, len(detected))

	for _, agentTemplate := range detected {
		agentBlock := map[string]any{"type": agentTemplate.Type}
		typeConfig := map[string]any{}
		if strings.TrimSpace(agentTemplate.Model) != "" {
			typeConfig["model"] = agentTemplate.Model
		}
		agentBlock[agentTemplate.Type] = typeConfig
		agents[agentTemplate.ID] = agentBlock
		poolMembers = append(poolMembers, agentTemplate.ID)
	}

	agents["pool"] = map[string]any{
		"type": "pool",
		"pool": map[string]any{
			"members": poolMembers,
		},
	}

	return agents
}

func buildBaldaInitProfiles(agentIDs []string) map[string]any {
	profiles := make(map[string]any, len(agentIDs))
	if len(agentIDs) == 0 {
		return profiles
	}

	for _, id := range agentIDs {
		profiles[id] = map[string]any{
			"balda": map[string]any{
				"provider": id,
			},
		}
	}

	return profiles
}

func ensureBaldaMCPServersDefault(baldaSection map[string]any) {
	raw, exists := baldaSection["mcp_servers"]
	if !exists || raw == nil {
		baldaSection["mcp_servers"] = []any{}
		return
	}
	if _, ok := raw.([]any); ok {
		return
	}
	if _, ok := raw.([]string); ok {
		return
	}
	baldaSection["mcp_servers"] = []any{}
}

func setBaldaProvider(doc map[string]any, providerID string) error {
	baldaSection, ok := toStringAnyMap(doc["balda"])
	if !ok {
		return fmt.Errorf("balda section is missing from generated config")
	}
	baldaSection["provider"] = providerID
	doc["balda"] = baldaSection

	return nil
}

func setBaldaTelegramToken(doc map[string]any, token string) error {
	baldaSection, ok := toStringAnyMap(doc["balda"])
	if !ok {
		return fmt.Errorf("balda section is missing from generated config")
	}
	telegramSection, ok := toStringAnyMap(baldaSection["telegram"])
	if !ok {
		return fmt.Errorf("balda.telegram section is missing from generated config")
	}
	telegramSection["token"] = token
	baldaSection["telegram"] = telegramSection
	doc["balda"] = baldaSection
	return nil
}

func setBaldaGlobalInstructionExample(doc map[string]any) error {
	baldaSection, ok := toStringAnyMap(doc["balda"])
	if !ok {
		return fmt.Errorf("balda section is missing from generated config")
	}

	if existing, exists := baldaSection["global_instruction"]; !exists || strings.TrimSpace(fmt.Sprintf("%v", existing)) == "" {
		baldaSection["global_instruction"] = baldaInitGlobalInstructionExample
	}

	doc["balda"] = baldaSection
	return nil
}

func setBaldaWorkspaceBaseBranch(baldaSection map[string]any, baseBranch string) error {
	workspaceSection, ok := toStringAnyMap(baldaSection["workspace"])
	if !ok {
		return fmt.Errorf("balda.workspace section is missing from generated config")
	}
	workspaceSection["base_branch"] = strings.TrimSpace(baseBranch)
	baldaSection["workspace"] = workspaceSection
	return nil
}

func baldaStateDirFromInitDocument(doc map[string]any) (string, error) {
	baldaSection, ok := toStringAnyMap(doc["balda"])
	if !ok {
		return "", fmt.Errorf("balda section is missing from generated config")
	}
	stateDirRaw, ok := baldaSection["state_dir"]
	if !ok {
		return baldaRuntimeStatePath, nil
	}
	stateDir := strings.TrimSpace(fmt.Sprintf("%v", stateDirRaw))
	if stateDir == "" {
		return "", fmt.Errorf("balda.state_dir is required")
	}
	return stateDir, nil
}

func toStringAnyMap(raw any) (map[string]any, bool) {
	switch v := raw.(type) {
	case map[string]any:
		return v, true
	case map[any]any:
		m := make(map[string]any, len(v))
		for key, value := range v {
			k, ok := key.(string)
			if !ok {
				return nil, false
			}
			m[k] = value
		}
		return m, true
	default:
		return nil, false
	}
}

var (
	baldaInitInput           io.Reader = os.Stdin
	baldaInitOutput          io.Writer = os.Stdout
	baldaInitIsInteractive             = defaultBaldaInitIsInteractive
	baldaInitLookPath                  = exec.LookPath
	baldaInitCurrentBranch             = func(workingDir string) (string, error) {
		return git.CurrentBranch(context.Background(), workingDir)
	}
	baldaInitLoadBotIdentity           = loadBotIdentityFromToken
)
