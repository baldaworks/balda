package sessionturn

import (
	"context"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/actors"
	"github.com/rs/zerolog"
)

func TestRunnerRequiresSessionManager(t *testing.T) {
	t.Parallel()

	runner := New(nil, nil, nil, zerolog.Nop())
	err := runner.RunSessionTurnPayload(context.Background(), actors.SessionTurnPayload{})
	if err == nil || !strings.Contains(err.Error(), "session manager is unavailable") {
		t.Fatalf("RunSessionTurnPayload() error = %v, want missing session manager", err)
	}
}
