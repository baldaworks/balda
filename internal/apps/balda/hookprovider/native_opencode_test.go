package hookprovider

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

func TestOpenCodeNativeDeniesToolBeforeSideEffect(t *testing.T) {
	if os.Getenv("BALDA_NATIVE_HOOK_TEST") != "1" {
		t.Skip("enable BALDA_NATIVE_HOOK_TEST with npm and published OpenCode 1.19")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 120*time.Second)
	defer cancel()
	directory := t.TempDir()
	service := testService(t, nativeDefinition(t, hookcmd.DecisionDeny))
	path, release, err := service.bind(kindOpenCode, hookcmd.Event{SessionID: "native-session", Provider: "provider", CWD: directory})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	var mu sync.Mutex
	toolRequested := false
	feedbackSeen := false
	modelServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		var request struct {
			Stream bool `json:"stream"`
			Tools  []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		if json.Unmarshal(data, &request) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		hasBash := false
		for _, tool := range request.Tools {
			if tool.Function.Name == "bash" {
				hasBash = true
			}
		}
		mu.Lock()
		callTool := hasBash && !toolRequested
		if callTool {
			toolRequested = true
		}
		if strings.Contains(string(data), "guard rejected this call") {
			feedbackSeen = true
		}
		mu.Unlock()
		if !request.Stream {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"id":"completion-probe","object":"chat.completion","created":1,"model":"native-model","choices":[{"index":0,"message":{"role":"assistant","content":"Native hook fixture"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		emit := func(delta any, finish any) {
			_, _ = fmt.Fprintf(w, "data: %s\n\n", encode(map[string]any{"id": "completion-fixture", "object": "chat.completion.chunk", "created": 1, "model": "native-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}))
		}
		if callTool {
			emit(map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "native-call-1", "type": "function", "function": map[string]string{"name": "bash", "arguments": `{"command":"touch native-side-effect","description":"Native hook fixture"}`}}}}, nil)
			emit(map[string]any{}, "tool_calls")
		} else {
			emit(map[string]string{"role": "assistant", "content": "Policy rejection verified."}, nil)
			emit(map[string]any{}, "stop")
		}
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer modelServer.Close()
	config := map[string]any{"model": "balda_hook_fixture/native-model", "small_model": "balda_hook_fixture/native-model", "provider": map[string]any{"balda_hook_fixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Balda hook fixture", "options": map[string]string{"baseURL": modelServer.URL, "apiKey": "synthetic-hook-fixture-key"}, "models": map[string]any{"native-model": map[string]any{"name": "Native hook fixture", "limit": map[string]int{"context": 32768, "output": 4096}}}}}}
	binary := buildNativeFixtureDispatcher(t, ctx, directory)
	runNativeACPFixture(t, ctx, binary, path, directory, []string{"npx", "-y", "opencode-ai@1.19.0", "acp"}, []string{
		"OPENCODE_CONFIG_CONTENT=" + string(encode(config)),
		"XDG_CONFIG_HOME=" + filepath.Join(directory, "config"), "XDG_DATA_HOME=" + filepath.Join(directory, "data"), "XDG_STATE_HOME=" + filepath.Join(directory, "state"), "XDG_CACHE_HOME=" + filepath.Join(directory, "cache"), "OPENCODE_DISABLE_MODELS_FETCH=true", "OPENCODE_DISABLE_AUTOUPDATE=true", "OPENCODE_DISABLE_LSP_DOWNLOAD=true",
	}, nil)
	mu.Lock()
	seen, called := feedbackSeen, toolRequested
	mu.Unlock()
	if !seen || !called {
		t.Fatal("native OpenCode did not report hook rejection")
	}
	if _, err := os.Stat(filepath.Join(directory, "native-side-effect")); !os.IsNotExist(err) {
		t.Fatalf("denied native tool caused a side effect: %v", err)
	}
}
