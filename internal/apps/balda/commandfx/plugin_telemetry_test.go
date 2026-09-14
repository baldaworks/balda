package commandfx

import (
	"context"
	"testing"

	commandplugin "github.com/baldaworks/balda/internal/apps/balda/actors/command/plugin"
	"github.com/rs/zerolog"
)

func TestPluginManagementTelemetryCountsBoundedEvents(t *testing.T) {
	t.Parallel()
	telemetry := NewPluginManagementTelemetry(zerolog.Nop())
	event := commandplugin.OperationEvent{Operation: "enable", Outcome: "success"}
	telemetry.ObservePluginOperation(context.Background(), event)
	telemetry.ObservePluginOperation(context.Background(), event)
	if got := telemetry.Counts()[event]; got != 2 {
		t.Fatalf("Counts()[%+v] = %d, want 2", event, got)
	}
}
