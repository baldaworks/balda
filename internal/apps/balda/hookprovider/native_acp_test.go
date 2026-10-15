package hookprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

func nativeEngineDefinition(t *testing.T) hookcmd.Definition {
	t.Helper()
	definition := nativeDefinition(t, hookcmd.DecisionDeny)
	if os.Getenv("BALDA_NATIVE_HOOK_SLOW_TEST") == "1" {
		definition.TimeoutSeconds = 3600
		definition.Args = append(definition.Args, "130")
		t.Log("verifying a real native decision after 130 seconds with a 3600-second hook deadline")
	}
	return definition
}

func buildNativeFixtureDispatcher(t *testing.T, ctx context.Context, directory string) string {
	t.Helper()
	binary := filepath.Join(directory, "balda")
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/balda")
	command.Dir = "../../../.."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build dispatcher: %v: %s", err, output)
	}
	return binary
}

func runNativeACPFixture(t *testing.T, ctx context.Context, binary, path, directory string, argv, environment []string, metadata map[string]any) {
	t.Helper()
	command := exec.CommandContext(ctx, binary, append([]string{"agent-hooks", "proxy", path, "--"}, argv...)...)
	command.Dir = directory
	command.Env = append(os.Environ(), environment...)
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	stderrPath := filepath.Join(directory, "native-stderr")
	stderr, err := os.Create(stderrPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	scanner := protocolScanner(stdout)
	rpc := func(id int, method string, params any) json.RawMessage {
		t.Helper()
		if _, err := stdin.Write(append(encode(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}), '\n')); err != nil {
			t.Fatal(err)
		}
		for scanner.Scan() {
			var message struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
				Result json.RawMessage `json:"result"`
				Error  json.RawMessage `json:"error"`
			}
			if json.Unmarshal(scanner.Bytes(), &message) != nil {
				t.Fatal("invalid native ACP response")
			}
			if message.Method == "session/request_permission" {
				var permission struct {
					Options []struct {
						OptionID string `json:"optionId"`
						Kind     string `json:"kind"`
					} `json:"options"`
				}
				_ = json.Unmarshal(message.Params, &permission)
				option := ""
				for _, candidate := range permission.Options {
					if candidate.Kind == "allow_once" {
						option = candidate.OptionID
						break
					}
				}
				_, _ = stdin.Write(append(encode(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option}}}), '\n'))
				continue
			}
			if string(message.ID) != fmt.Sprint(id) {
				continue
			}
			if len(message.Error) != 0 {
				t.Fatalf("native ACP error: %s", message.Error)
			}
			return message.Result
		}
		data, _ := os.ReadFile(stderrPath)
		t.Fatalf("native ACP stream ended: %v: %s", scanner.Err(), data)
		return nil
	}
	_ = rpc(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "balda-native-hook-fixture", "version": "1"}})
	params := map[string]any{"cwd": directory, "mcpServers": []any{}}
	if metadata != nil {
		params["_meta"] = metadata
	}
	created := rpc(2, "session/new", params)
	var session struct {
		ID string `json:"sessionId"`
	}
	if json.Unmarshal(created, &session) != nil || session.ID == "" {
		t.Fatalf("native session: %s", created)
	}
	_ = rpc(3, "session/prompt", map[string]any{"sessionId": session.ID, "prompt": []any{map[string]string{"type": "text", "text": "Run one local command."}}})
}
