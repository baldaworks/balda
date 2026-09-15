// Package mcpfx adapts catalog MCP projections to provider runtime registries.
package mcpfx

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/mcpregistry"
)

// RegistryProjector updates desired configs for newly constructed runtimes.
// It never restarts an active provider runtime implicitly.
type RegistryProjector struct {
	registry mcpregistry.Registry
}

// NewRegistryProjector creates a Norma registry projection adapter.
func NewRegistryProjector(registry mcpregistry.Registry) (*RegistryProjector, error) {
	if registry == nil {
		return nil, errors.New("MCP registry is required")
	}
	return &RegistryProjector{registry: registry}, nil
}

// Project registers a revision-qualified config for new runtimes.
func (p *RegistryProjector) Project(_ context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (runtimecatalogcmd.MCPProjectionOutcome, error) {
	providerConfig, err := providerConfig(config)
	if err != nil {
		return runtimecatalogcmd.MCPProjectionUnsupported, err
	}
	p.registry.Set(RegistryID(key), providerConfig)
	return runtimecatalogcmd.MCPProjectionNewRuntimesOnly, nil
}

// Remove prevents new runtimes from selecting a drained revision.
func (p *RegistryProjector) Remove(_ context.Context, key mcpruntime.InstanceKey) error {
	p.registry.Delete(RegistryID(key))
	return nil
}

// RegistryID returns an opaque deterministic provider registry identity.
func RegistryID(key mcpruntime.InstanceKey) string {
	hash := sha256.New()
	_, _ = hash.Write([]byte(key.Source.Kind))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(key.Source.Name))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(key.Revision))
	_, _ = hash.Write([]byte{0})
	_, _ = hash.Write([]byte(key.Name))
	return "balda.catalog." + hex.EncodeToString(hash.Sum(nil))
}

func providerConfig(config mcpruntime.LaunchConfig) (agentconfig.MCPServerConfig, error) {
	switch config.Transport {
	case "stdio":
		return agentconfig.MCPServerConfig{
			Type: agentconfig.MCPServerTypeStdio,
			Cmd:  []string{config.Command}, Args: append([]string(nil), config.Args...),
			Env: cloneMap(config.Env), WorkingDir: config.WorkingDir,
		}, nil
	case "streamable-http":
		return agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeHTTP, URL: config.URL, Headers: cloneMap(config.Headers)}, nil
	case "sse":
		return agentconfig.MCPServerConfig{Type: agentconfig.MCPServerTypeSSE, URL: config.URL, Headers: cloneMap(config.Headers)}, nil
	default:
		return agentconfig.MCPServerConfig{}, errors.New("provider MCP transport is unsupported")
	}
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

var _ mcpruntime.Projector = (*RegistryProjector)(nil)
