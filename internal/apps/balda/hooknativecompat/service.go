package hooknativecompat

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/normahq/runtime/v2/agentconfig"
)

// Configuration is the transitional adapter's read-only live policy port.
type Configuration interface {
	Load(ctx context.Context) (hookcmd.Record, error)
}

// Service preserves the current native launch integration until registered
// provider adapters replace it in TASK-004 and TASK-005.
type Service struct {
	bindings   *hookprovider.Service
	records    Configuration
	root       string
	providers  map[string]agentconfig.Config
	executable string
	mu         sync.Mutex
	sessions   map[string]int
}

// New creates the temporary provider-aware launch adapter around neutral bindings.
func New(runner hookprovider.Runner, records Configuration, root string, providers map[string]agentconfig.Config) (*Service, error) {
	if records == nil {
		return nil, hookcmd.ErrUnavailable
	}
	bindings, err := hookprovider.New(runner)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		_ = bindings.Close()
		return nil, err
	}
	return &Service{bindings: bindings, records: records, root: root, providers: maps.Clone(providers), executable: executable, sessions: make(map[string]int)}, nil
}

func providerKind(kind string) string {
	switch kind {
	case agentconfig.AgentTypeClaudeCodeACP, agentconfig.AgentTypeClaudeACP:
		return kindClaude
	case agentconfig.AgentTypeCodexACP:
		return kindCodex
	case agentconfig.AgentTypeOpenCodeACP:
		return kindOpenCode
	default:
		return ""
	}
}

func leaves(providers map[string]agentconfig.Config, root, executable string) ([]string, error) {
	var ids []string
	visiting := make(map[string]bool)
	visited := make(map[string]bool)
	var visit func(string) error
	visit = func(id string) error {
		if visiting[id] {
			return hookcmd.ErrUnsupported
		}
		if visited[id] {
			return nil
		}
		config, ok := providers[id]
		if !ok {
			return hookcmd.ErrUnsupported
		}
		resolved, err := agentconfig.NormalizeConfig(config, executable)
		if err != nil {
			return hookcmd.ErrUnsupported
		}
		visiting[id] = true
		if agentconfig.IsPoolType(config.Type) {
			for _, member := range resolved.PoolMembers {
				if err := visit(member); err != nil {
					return err
				}
			}
		} else {
			ids = append(ids, id)
		}
		delete(visiting, id)
		visited[id] = true
		return nil
	}
	if err := visit(root); err != nil {
		return nil, err
	}
	slices.Sort(ids)
	return ids, nil
}

// ValidateDefinition rejects native interception unsupported by the current launch adapter.
func (s *Service) ValidateDefinition(_ context.Context, definition hookcmd.Definition) error {
	if !definition.Event.ToolEvent() || !definition.Enabled {
		return nil
	}
	if !hookprovider.NativePlatformSupported() || definition.Provider != "" && definition.Provider != s.root {
		return hookcmd.ErrUnsupported
	}
	ids, err := leaves(s.providers, s.root, s.executable)
	if err != nil || len(ids) == 0 {
		return hookcmd.ErrUnsupported
	}
	for _, id := range ids {
		if providerKind(s.providers[id].Type) == "" {
			return hookcmd.ErrUnsupported
		}
	}
	return nil
}

// Prepare installs the transitional provider-owned proxy around selected leaves.
func (s *Service) Prepare(ctx context.Context, sessionID, providerID, cwd string, providers map[string]agentconfig.Config) (map[string]agentconfig.Config, func() error, error) {
	if sessionID == "" || strings.TrimSpace(cwd) == "" {
		return nil, nil, hookcmd.ErrInvalid
	}
	ids, err := leaves(providers, providerID, s.executable)
	if err != nil {
		return nil, nil, err
	}
	record, err := s.records.Load(ctx)
	if err != nil || record.Validate() != nil {
		return nil, nil, hookcmd.ErrUnavailable
	}
	nativeRequired := false
	for _, definition := range record.Definitions {
		if !definition.Enabled || !definition.Event.ToolEvent() || definition.Provider != "" && definition.Provider != providerID {
			continue
		}
		nativeRequired = true
		for _, id := range ids {
			if providerKind(providers[id].Type) == "" {
				return nil, nil, hookcmd.ErrUnsupported
			}
		}
	}
	if !nativeRequired {
		return maps.Clone(providers), func() error { return nil }, nil
	}
	if !hookprovider.NativePlatformSupported() {
		return nil, nil, hookcmd.ErrUnsupported
	}
	copyProviders := maps.Clone(providers)
	var bindings []*hookprovider.Binding
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			for _, binding := range bindings {
				releaseErr = errors.Join(releaseErr, binding.Release())
			}
			s.mu.Lock()
			s.sessions[sessionID] -= len(bindings)
			if s.sessions[sessionID] <= 0 {
				delete(s.sessions, sessionID)
			}
			s.mu.Unlock()
		})
		return releaseErr
	}
	for _, id := range ids {
		config := providers[id]
		kind := providerKind(config.Type)
		if kind == "" {
			continue
		}
		resolved, err := agentconfig.NormalizeConfig(config, s.executable)
		if err != nil {
			_ = release()
			return nil, nil, err
		}
		binding, err := s.bindings.Bind(ctx, hookprovider.BindingScope{SessionID: sessionID, Provider: providerID, CWD: cwd})
		if err != nil {
			_ = release()
			return nil, nil, err
		}
		if err := os.WriteFile(binding.Reference()+".kind", []byte(kind), 0600); err != nil {
			_ = binding.Release()
			_ = release()
			return nil, nil, err
		}
		if err := os.WriteFile(binding.Reference()+".owner-proof", []byte(binding.OwnerProof()), 0600); err != nil {
			_ = binding.Release()
			_ = release()
			return nil, nil, err
		}
		bindings = append(bindings, binding)
		config.Type = agentconfig.AgentTypeGenericACP
		config.GenericACP = &agentconfig.ACPConfig{Cmd: append([]string{s.executable, "agent-hooks", "proxy", binding.Reference(), binding.OwnerProof(), "--"}, resolved.Command...), Model: resolved.Model, ModelConfigID: resolved.ModelConfigID, ReasoningEffort: resolved.ReasoningEffort, ReasoningEffortConfigID: resolved.ReasoningEffortConfigID, Mode: resolved.Mode}
		config.ClaudeCodeACP, config.CodexACP, config.OpenCodeACP = nil, nil, nil
		copyProviders[id] = config
	}
	s.mu.Lock()
	s.sessions[sessionID] += len(bindings)
	s.mu.Unlock()
	return copyProviders, release, nil
}

func (s *Service) bind(kind string, event hookcmd.Event) (string, func() error, error) {
	binding, err := s.bindings.Bind(context.Background(), hookprovider.BindingScope{SessionID: event.SessionID, Provider: event.Provider, CWD: event.CWD})
	if err != nil {
		return "", nil, err
	}
	if err := os.WriteFile(binding.Reference()+".kind", []byte(kind), 0600); err != nil {
		_ = binding.Release()
		return "", nil, err
	}
	if err := os.WriteFile(binding.Reference()+".owner-proof", []byte(binding.OwnerProof()), 0600); err != nil {
		_ = binding.Release()
		return "", nil, err
	}
	return binding.Reference(), binding.Release, nil
}

// CapabilityDescription describes support without probing provider credentials.
func (s *Service) CapabilityDescription() string {
	if s.ValidateDefinition(context.Background(), hookcmd.Definition{Enabled: true, Event: hookcmd.PreToolUse}) != nil {
		return "Native tool hooks are unavailable for the configured provider. Lifecycle hooks remain available."
	}
	return fmt.Sprintf("Native PreToolUse and PostToolUse adapters are configured for provider %s. Activation requires a new session: use /reset for an existing session. In-flight tool calls are unaffected. Native engine deadlines still apply; Claude's outer timer is about 24.8 days. Unsupported native versions fail explicitly.", s.root)
}

// VerifySession blocks turns in sessions created before native activation.
func (s *Service) VerifySession(ctx context.Context, sessionID string) error {
	record, err := s.records.Load(ctx)
	if err != nil || record.Validate() != nil {
		return hookcmd.ErrUnavailable
	}
	required := false
	for _, definition := range record.Definitions {
		if definition.Enabled && definition.Event.ToolEvent() && (definition.Provider == "" || definition.Provider == s.root) {
			required = true
			break
		}
	}
	if !required {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessions[sessionID] > 0 {
		return nil
	}
	return hookcmd.ErrSessionReset
}

// Close releases every neutral binding owned by the compatibility service.
func (s *Service) Close() error { return s.bindings.Close() }
