package commandfx

import (
	"context"
	"sync"

	commandplugin "github.com/baldaworks/balda/internal/apps/balda/actors/command/plugin"
	"github.com/rs/zerolog"
)

// PluginManagementTelemetry emits redacted metric events and retains process
// counters for a metrics collector. Keys contain only fixed operations/outcomes.
type PluginManagementTelemetry struct {
	mu     sync.RWMutex
	counts map[commandplugin.OperationEvent]uint64
	logger zerolog.Logger
}

func NewPluginManagementTelemetry(logger zerolog.Logger) *PluginManagementTelemetry {
	return &PluginManagementTelemetry{
		counts: make(map[commandplugin.OperationEvent]uint64),
		logger: logger.With().Str("component", "balda.commandfx.plugin_metrics").Logger(),
	}
}

func (m *PluginManagementTelemetry) ObservePluginOperation(_ context.Context, event commandplugin.OperationEvent) {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.counts[event]++
	m.mu.Unlock()
	m.logger.Info().Str("metric", "balda_plugin_management_operations_total").Str("operation", event.Operation).Str("outcome", event.Outcome).Msg("plugin management metric")
}

// Counts returns a copy suitable for a metrics exporter or status collector.
func (m *PluginManagementTelemetry) Counts() map[commandplugin.OperationEvent]uint64 {
	if m == nil {
		return nil
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[commandplugin.OperationEvent]uint64, len(m.counts))
	for event, count := range m.counts {
		out[event] = count
	}
	return out
}
