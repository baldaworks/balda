package handlers

import (
	"strings"
	"testing"
)

func TestBuildAgentWelcomeMessage_FormatsStableKVLine(t *testing.T) {
	msg := BuildAgentWelcomeMessage("topic-alpha", "tg-1-2", "opencode_acp", "gpt-5", []string{"mcp.one", "mcp.two"})
	want := "🚀 **Session Started** • **Name:** `topic-alpha` • **ID:** `tg-1-2` • **Model:** `gpt-5` • **Type:** `opencode_acp` • **MCP:** `mcp.one, mcp.two` "
	if msg != want {
		t.Fatalf("message = %q, want %q", msg, want)
	}
}

func TestBuildAgentWelcomeMessage_UsesNonePlaceholders(t *testing.T) {
	msg := BuildAgentWelcomeMessage(" ", " ", " ", " ", nil)
	if !strings.Contains(msg, "**Name:** `none`") {
		t.Fatalf("message = %q, want **Name:** `none` ", msg)
	}
	if !strings.Contains(msg, "**ID:** `none`") {
		t.Fatalf("message = %q, want **ID:** `none` ", msg)
	}
	if !strings.Contains(msg, "**Type:** `none`") {
		t.Fatalf("message = %q, want **Type:** `none` ", msg)
	}
	if !strings.Contains(msg, "**Model:** `none`") {
		t.Fatalf("message = %q, want **Model:** `none` ", msg)
	}
	if !strings.Contains(msg, "**MCP:** `none`") {
		t.Fatalf("message = %q, want **MCP:** `none` ", msg)
	}
}
