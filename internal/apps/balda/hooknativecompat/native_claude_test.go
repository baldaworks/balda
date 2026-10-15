package hooknativecompat

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

func TestClaudeNativeDeniesToolBeforeSideEffect(t *testing.T) {
	if os.Getenv("BALDA_NATIVE_HOOK_TEST") != "1" {
		t.Skip("enable BALDA_NATIVE_HOOK_TEST with npm and the published Claude ACP bridge")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	directory := t.TempDir()
	binary := buildNativeFixtureDispatcher(t, ctx, directory)
	service := testService(t, nativeEngineDefinition(t))
	path, release, err := service.bind(kindClaude, hookcmd.Event{SessionID: "native-session", Provider: "provider", CWD: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	var mu sync.Mutex
	requests := 0
	feedbackSeen := false
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if !strings.HasSuffix(r.URL.Path, "/messages") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"input_tokens":1}`)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
		}
		if json.Unmarshal(data, &request) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"msg_probe","type":"message","role":"assistant","model":"claude-sonnet-4-6","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","stop_sequence":null,"usage":{"input_tokens":1,"output_tokens":1}}`)
			return
		}
		mu.Lock()
		requests++
		number := requests
		if strings.Contains(string(data), "guard rejected this call") {
			feedbackSeen = true
		}
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(event string, value any) { _, _ = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, encode(value)) }
		message := map[string]any{"id": fmt.Sprintf("msg_%d", number), "type": "message", "role": "assistant", "model": "claude-sonnet-4-6", "content": []any{}, "stop_reason": nil, "stop_sequence": nil, "usage": map[string]int{"input_tokens": 1, "output_tokens": 0}}
		emit("message_start", map[string]any{"type": "message_start", "message": message})
		stopReason := "end_turn"
		if number == 1 {
			stopReason = "tool_use"
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "tool_use", "id": "toolu_native_call", "name": "Bash", "input": map[string]any{}}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "input_json_delta", "partial_json": `{"command":"touch native-side-effect","description":"Native hook fixture"}`}})
		} else {
			emit("content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
			emit("content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": "Policy rejection verified."}})
		}
		emit("content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		emit("message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopReason, "stop_sequence": nil}, "usage": map[string]int{"output_tokens": 1}})
		emit("message_stop", map[string]any{"type": "message_stop"})
	}))
	defer modelServer.Close()
	runNativeACPFixture(t, ctx, binary, path, directory, []string{"npx", "-y", "@zed-industries/claude-code-acp@0.16.2"}, []string{
		"CLAUDE_CONFIG_DIR=" + filepath.Join(directory, "native-config"), "ANTHROPIC_BASE_URL=" + modelServer.URL, "ANTHROPIC_API_KEY=synthetic-hook-fixture-key", "ANTHROPIC_AUTH_TOKEN=", "CLAUDE_CODE_OAUTH_TOKEN=", "CLAUDE_CODE_USE_BEDROCK=0", "CLAUDE_CODE_USE_VERTEX=0", "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1", "DISABLE_AUTOUPDATER=1",
	}, map[string]any{"claudeCode": map[string]any{"options": map[string]any{"settingSources": []any{}, "model": "claude-sonnet-4-6"}}})
	mu.Lock()
	seen, count := feedbackSeen, requests
	mu.Unlock()
	if !seen || count < 2 {
		t.Fatalf("native Claude did not report hook rejection (requests=%d)", count)
	}
	if _, err := os.Stat(filepath.Join(directory, "native-side-effect")); !os.IsNotExist(err) {
		t.Fatalf("denied native tool caused a side effect: %v", err)
	}
}
