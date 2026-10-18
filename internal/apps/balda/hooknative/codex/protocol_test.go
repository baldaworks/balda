package codex

import (
	"strings"
	"testing"
)

func TestHookArgumentsOwnDispatchReadinessAndEnvelope(t *testing.T) {
	arguments := codexHookArguments()
	joined := strings.Join(arguments, "\n")
	for _, want := range []string{"hooks.PreToolUse", "hooks.PostToolUse", "hooks.SessionStart", "agent-hooks dispatch codex", "agent-hooks ready codex", "timeout=9223372036"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("hook arguments %q do not contain %q", arguments, want)
		}
	}
}
