package hookprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// This exercises the installed native hook engine against a loopback Responses
// fixture. It makes no model requests to an external service and uses an isolated
// native config directory so user hooks, credentials and history are untouched.
func TestCodexNativeDeniesToolBeforeSideEffect(t *testing.T) {
	if os.Getenv("BALDA_NATIVE_HOOK_TEST") != "1" {
		t.Skip("enable BALDA_NATIVE_HOOK_TEST with a supported native Codex CLI")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 4*time.Minute)
	defer cancel()
	directory := t.TempDir()
	nativeConfigDir := filepath.Join(directory, "native-config")
	if err := os.Mkdir(nativeConfigDir, 0700); err != nil {
		t.Fatal(err)
	}
	binary := filepath.Join(directory, "balda")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/balda")
	build.Dir = "../../../.."
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build dispatcher: %v: %s", err, output)
	}
	service := testService(t, nativeEngineDefinition(t))
	path, release, err := service.bind(kindCodex, hookcmd.Event{SessionID: "native-session", Provider: "provider", CWD: directory})
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
		var request struct {
			Tools []struct {
				Name string `json:"name"`
			} `json:"tools"`
		}
		if json.Unmarshal(data, &request) != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
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
		responseID := fmt.Sprintf("response-%d", number)
		emit("response.created", map[string]any{"type": "response.created", "response": map[string]any{"id": responseID, "status": "in_progress", "output": []any{}}})
		var item map[string]any
		if number == 1 {
			name := "exec_command"
			for _, tool := range request.Tools {
				if tool.Name == "exec_command" || tool.Name == "shell_command" {
					name = tool.Name
					break
				}
			}
			arguments := map[string]any{"cmd": "touch native-side-effect", "yield_time_ms": 1000, "max_output_tokens": 1000}
			if name == "shell_command" {
				arguments = map[string]any{"command": "touch native-side-effect", "timeout_ms": 1000, "workdir": directory}
			}
			item = map[string]any{"id": "function-1", "type": "function_call", "call_id": "native-call-1", "name": name, "arguments": string(encode(arguments))}
		} else {
			item = map[string]any{"id": "message-1", "type": "message", "role": "assistant", "status": "completed", "content": []any{map[string]any{"type": "output_text", "text": "Policy rejection verified."}}}
		}
		emit("response.output_item.added", map[string]any{"type": "response.output_item.added", "output_index": 0, "item": item})
		emit("response.output_item.done", map[string]any{"type": "response.output_item.done", "output_index": 0, "item": item})
		emit("response.completed", map[string]any{"type": "response.completed", "response": map[string]any{"id": responseID, "status": "completed", "output": []any{item}}})
	}))
	defer modelServer.Close()
	prepared, err := prepareCodex(ctx, []string{"codex-acp"})
	if err != nil {
		t.Fatal(err)
	}
	var arguments []string
	for _, arg := range prepared[1:] {
		arguments = append(arguments, strings.TrimPrefix(arg, "--codex-args="))
	}
	arguments = append(arguments, "-c", `model_provider="balda_hook_fixture"`, "-c", `model="gpt-5.4"`, "-c", `features.code_mode=false`, "-c", `features.plugins=false`, "-c", `model_providers.balda_hook_fixture={name="Balda hook fixture",base_url="`+modelServer.URL+`",wire_api="responses",requires_openai_auth=false}`, "exec", "--json", "--skip-git-repo-check", "--ephemeral", "--sandbox", "workspace-write", "Run one local command.")
	command := exec.CommandContext(ctx, "codex", arguments...)
	command.Dir = directory
	configureProxyProcess(command)
	defer func() {
		if command.Process != nil {
			_ = killNativeGroup(command.Process.Pid)
		}
	}()
	command.Env = append(os.Environ(), "CODEX_HOME="+nativeConfigDir, bindingEnv+"="+path, executableEnv+"="+binary)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("native Codex: %v: %s", err, output)
	}
	mu.Lock()
	seen := feedbackSeen
	count := requests
	mu.Unlock()
	if !seen || count < 2 {
		t.Fatalf("native engine did not report hook rejection (requests=%d): %s", count, output)
	}
	if _, err := os.Stat(filepath.Join(directory, "native-side-effect")); !os.IsNotExist(err) {
		t.Fatalf("denied native tool caused a side effect: %v", err)
	}
}
