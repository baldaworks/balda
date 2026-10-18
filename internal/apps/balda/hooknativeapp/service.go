// Package hooknativeapp composes native policy, bindings, and registered adapters.
package hooknativeapp

import (
	"context"
	"fmt"
	"maps"
	"os"
	"strings"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknativehost"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/runtime/v2/agentconfig"
)

type Configuration interface {
	Load(ctx context.Context) (hookcmd.Record, error)
}
type AdapterFactory func(hooknativehost.Host) hooknative.Adapter

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

func New(runner hookprovider.Runner, records Configuration, root string, providers map[string]agentconfig.Config, factories ...AdapterFactory) (*Service, error) {
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
	s := &Service{bindings: bindings, records: records, root: root, providers: maps.Clone(providers), executable: executable, sessions: make(map[string]int)}
	adapters := make([]hooknative.Adapter, 0, len(factories))
	for _, factory := range factories {
		adapters = append(adapters, factory(s))
	}
	s.registry, err = hooknative.NewRegistry(adapters...)
	if err != nil {
		_ = bindings.Close()
		return nil, err
	}
	return s, nil
}

func (s *Service) Bind(ctx context.Context, scope hookprovider.BindingScope) (*hookprovider.Binding, error) {
	return s.bindings.Bind(ctx, scope)
}
func (s *Service) Executable() string { return s.executable }

func (s *Service) ValidateDefinition(_ context.Context, definition hookcmd.Definition) error {
	if !definition.Event.ToolEvent() || !definition.Enabled {
		return nil
	}
	if !hookprovider.NativePlatformSupported() || definition.Provider != "" && definition.Provider != s.root {
		return hookcmd.ErrUnsupported
	}
	config, ok := s.providers[s.root]
	if !ok || !agentconfig.IsPoolType(config.Type) && !agentconfig.IsACPType(config.Type) {
		return hookcmd.ErrUnsupported
	}
	if !agentconfig.IsPoolType(config.Type) && s.registry.Validate(config) != nil {
		return hookcmd.ErrUnsupported
	}
	return nil
}

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

func (s *Service) Prepare(ctx context.Context, request hooknative.LeafRequest) (hooknative.PreparedLeaf, error) {
	prepared, err := s.registry.Prepare(ctx, request)
	if err != nil {
		return prepared, err
	}
	s.mu.Lock()
	s.sessions[request.SessionID]++
	s.mu.Unlock()
	underlying := prepared.Release
	var once sync.Once
	var releaseErr error
	prepared.Release = func() error {
		once.Do(func() {
			if underlying != nil {
				releaseErr = underlying()
			}
			s.mu.Lock()
			s.sessions[request.SessionID]--
			if s.sessions[request.SessionID] <= 0 {
				delete(s.sessions, request.SessionID)
			}
			s.mu.Unlock()
		})
		return releaseErr
	}
	return prepared, nil
}

func (s *Service) CapabilityDescription() string {
	if s.ValidateDefinition(context.Background(), hookcmd.Definition{Enabled: true, Event: hookcmd.PreToolUse}) != nil {
		return "Native tool hooks are unavailable for the configured provider. Lifecycle hooks remain available."
	}
	return fmt.Sprintf("Native PreToolUse and PostToolUse adapters are configured for provider %s. Activation requires a new session: use /reset for an existing session. In-flight tool calls are unaffected. Native engine deadlines still apply; Claude's outer timer is about 24.8 days. Unsupported native versions fail explicitly.", s.root)
}

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

func (s *Service) Close() error { return s.bindings.Close() }

func ValidateRequest(request hooknative.LeafRequest) error {
	if request.SessionID == "" || strings.TrimSpace(request.CWD) == "" || request.LeafID == "" {
		return hookcmd.ErrInvalid
	}
	if !hookprovider.NativePlatformSupported() {
		return hookcmd.ErrUnsupported
	}
	return nil
}
