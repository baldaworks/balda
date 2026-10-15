package hooknativecompat

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
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
	registry   *hooknative.Registry
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
	service := &Service{bindings: bindings, records: records, root: root, providers: maps.Clone(providers), executable: executable, sessions: make(map[string]int)}
	service.registry, err = hooknative.NewRegistry(compatAdapter{service: service})
	if err != nil {
		_ = bindings.Close()
		return nil, err
	}
	return service, nil
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

// ValidateDefinition rejects native interception unsupported by the current launch adapter.
func (s *Service) ValidateDefinition(_ context.Context, definition hookcmd.Definition) error {
	if !definition.Event.ToolEvent() || !definition.Enabled {
		return nil
	}
	if !hookprovider.NativePlatformSupported() || definition.Provider != "" && definition.Provider != s.root {
		return hookcmd.ErrUnsupported
	}
	config, ok := s.providers[s.root]
	if !ok {
		return hookcmd.ErrUnsupported
	}
	// catalogapp owns traversal for pools; leaf capability is checked by the
	// registry when a session is prepared.
	if !agentconfig.IsPoolType(config.Type) && (!agentconfig.IsACPType(config.Type) || providerKind(config.Type) == "") {
		return hookcmd.ErrUnsupported
	}
	return nil
}

// Required reports whether the selected root has active native tool policy.
func (s *Service) Required(ctx context.Context, providerID string) (bool, error) {
	record, err := s.records.Load(ctx)
	if err != nil || record.Validate() != nil {
		return false, hookcmd.ErrUnavailable
	}
	for _, definition := range record.Definitions {
		if definition.Enabled && definition.Event.ToolEvent() && (definition.Provider == "" || definition.Provider == providerID) {
			return true, nil
		}
	}
	return false, nil
}

// Prepare selects the compatibility adapter for one catalog-owned ACP leaf.
func (s *Service) Prepare(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	return s.registry.Prepare(ctx, request)
}

type compatAdapter struct{ service *Service }

func (compatAdapter) Name() string { return "compat" }

func (compatAdapter) Supports(config agentconfig.Config) bool { return providerKind(config.Type) != "" }

func (a compatAdapter) Prepare(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	return a.service.PrepareAdapter(ctx, request)
}

// PrepareAdapter installs the transitional proxy for one ACP leaf.
func (s *Service) PrepareAdapter(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	if request.SessionID == "" || strings.TrimSpace(request.CWD) == "" || request.LeafID == "" {
		return hooknative.PreparedLeaf{}, hookcmd.ErrInvalid
	}
	if !hookprovider.NativePlatformSupported() {
		return hooknative.PreparedLeaf{}, hookcmd.ErrUnsupported
	}
	resolved, err := agentconfig.NormalizeConfig(request.Config, s.executable)
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	binding, err := s.bindings.Bind(ctx, hookprovider.BindingScope{SessionID: request.SessionID, Provider: request.ProviderID, CWD: request.CWD})
	if err != nil {
		return hooknative.PreparedLeaf{}, err
	}
	if err := os.WriteFile(binding.Reference()+".kind", []byte(providerKind(request.Config.Type)), 0600); err != nil {
		_ = binding.Release()
		return hooknative.PreparedLeaf{}, err
	}
	if err := os.WriteFile(binding.Reference()+".owner-proof", []byte(binding.OwnerProof()), 0600); err != nil {
		_ = binding.Release()
		return hooknative.PreparedLeaf{}, err
	}
	s.mu.Lock()
	s.sessions[request.SessionID]++
	s.mu.Unlock()
	var once sync.Once
	var releaseErr error
	release := func() error {
		once.Do(func() {
			releaseErr = binding.Release()
			s.mu.Lock()
			s.sessions[request.SessionID]--
			if s.sessions[request.SessionID] <= 0 {
				delete(s.sessions, request.SessionID)
			}
			s.mu.Unlock()
		})
		return releaseErr
	}
	config := request.Config
	config.Type = agentconfig.AgentTypeGenericACP
	config.GenericACP = &agentconfig.ACPConfig{Cmd: append([]string{s.executable, "agent-hooks", "proxy", binding.Reference(), binding.OwnerProof(), "--"}, resolved.Command...), Model: resolved.Model, ModelConfigID: resolved.ModelConfigID, ReasoningEffort: resolved.ReasoningEffort, ReasoningEffortConfigID: resolved.ReasoningEffortConfigID, Mode: resolved.Mode}
	config.ClaudeCodeACP, config.CodexACP, config.OpenCodeACP = nil, nil, nil
	return hooknative.PreparedLeaf{Config: config, Release: release}, nil
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
