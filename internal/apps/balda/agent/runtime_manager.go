package agent

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/normahq/balda/internal/apps/balda/shutdown"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

// RuntimeManager owns the single app-scoped balda provider runtime.
type RuntimeManager struct {
	builder           *Builder
	providerID        string
	workingDir        string
	baldaMCPServerIDs []string
	logger            zerolog.Logger

	mu      sync.RWMutex
	runtime *BuiltRuntime
}

// RuntimeManagerParams wires RuntimeManager dependencies.
type RuntimeManagerParams struct {
	fx.In

	LC                fx.Lifecycle
	Builder           *Builder
	BaldaProviderID   string `name:"balda_provider"`
	WorkingDir        string
	BaldaMCPServerIDs []string `name:"balda_mcp_servers"`
	Logger            zerolog.Logger
}

// NewRuntimeManager creates the app-scoped balda runtime owner.
func NewRuntimeManager(p RuntimeManagerParams) *RuntimeManager {
	m := &RuntimeManager{
		builder:           p.Builder,
		providerID:        strings.TrimSpace(p.BaldaProviderID),
		workingDir:        strings.TrimSpace(p.WorkingDir),
		baldaMCPServerIDs: append([]string(nil), p.BaldaMCPServerIDs...),
		logger:            p.Logger.With().Str("component", "balda.runtime_manager").Logger(),
	}

	p.LC.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return m.close()
		},
	})

	return m
}

// ProviderID returns the configured balda provider ID.
func (m *RuntimeManager) ProviderID() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.providerID
}

// EnsureRuntime initializes the runtime if it has not been created yet.
func (m *RuntimeManager) EnsureRuntime(ctx context.Context) error {
	_, err := m.Runtime(ctx)
	return err
}

// Runtime returns the cached app-scoped runtime, creating it on first use.
func (m *RuntimeManager) Runtime(ctx context.Context) (*BuiltRuntime, error) {
	m.mu.RLock()
	if m.runtime != nil {
		runtime := m.runtime
		m.mu.RUnlock()
		return runtime, nil
	}
	builder := m.builder
	providerID := strings.TrimSpace(m.providerID)
	workingDir := m.workingDir
	extraMCPServerIDs := append([]string(nil), m.baldaMCPServerIDs...)
	m.mu.RUnlock()

	if builder == nil {
		return nil, fmt.Errorf("agent builder is required")
	}
	if providerID == "" {
		return nil, fmt.Errorf("balda provider is not configured")
	}

	runtime, err := builder.BuildRuntimeWithMCPServerIDs(
		ctx,
		providerID,
		workingDir,
		nil,
		extraMCPServerIDs,
	)
	if err != nil {
		m.logger.Error().Err(err).Str("agent", providerID).Msg("failed to build balda provider runtime")
		return nil, err
	}

	m.mu.Lock()
	if existing := m.runtime; existing != nil {
		m.mu.Unlock()
		if closeErr := closeBuiltRuntime(runtime); closeErr != nil {
			m.logger.Warn().Err(closeErr).Str("agent", providerID).Msg("failed to close duplicate balda provider runtime")
		}
		return existing, nil
	}
	m.runtime = runtime
	m.mu.Unlock()

	m.logger.Info().Str("agent", providerID).Msg("balda provider runtime ready")
	return runtime, nil
}

func (m *RuntimeManager) close() error {
	m.mu.Lock()
	runtime := m.runtime
	m.runtime = nil
	m.mu.Unlock()
	return closeBuiltRuntime(runtime)
}

func closeBuiltRuntime(runtime *BuiltRuntime) error {
	if runtime == nil {
		return nil
	}
	return closeRuntimeAgent(runtime.Agent)
}

func closeRuntimeAgent(agent any) error {
	closer, ok := agent.(io.Closer)
	if !ok {
		return nil
	}
	if err := closer.Close(); err != nil {
		if shutdown.IsExpected(err) {
			return nil
		}
		return fmt.Errorf("close balda provider runtime agent: %w", err)
	}
	return nil
}
