package proxyruntime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

func nativeBinding(t *testing.T, adapter, cwd string) (string, string, func() error, error) {
	t.Helper()
	service, err := hookprovider.New(runnerFunc(func(ctx context.Context, _ hookcmd.Event) (hookcmd.Result, error) {
		if os.Getenv("BALDA_NATIVE_HOOK_SLOW_TEST") == "1" {
			select {
			case <-time.After(130 * time.Second):
			case <-ctx.Done():
				return hookcmd.Result{}, ctx.Err()
			}
		}
		return hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: "guard rejected this call"}, nil
	}))
	if err != nil {
		return "", "", nil, err
	}
	binding, err := service.Bind(t.Context(), hookprovider.BindingScope{SessionID: "native-session", Provider: "provider", CWD: cwd})
	if err != nil {
		_ = service.Close()
		return "", "", nil, err
	}
	if err := writeMetadata(binding, adapter); err != nil {
		_ = binding.Release()
		_ = service.Close()
		return "", "", nil, err
	}
	release := func() error {
		err := binding.Release()
		if closeErr := service.Close(); err == nil {
			err = closeErr
		}
		return err
	}
	return binding.Reference(), binding.OwnerProof(), release, nil
}
func buildNativeFixtureDispatcher(t *testing.T, ctx context.Context, directory string) string {
	t.Helper()
	binary := filepath.Join(directory, "balda")
	command := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/balda")
	command.Dir = "../../../../.."
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build dispatcher: %v: %s", err, output)
	}
	return binary
}
func runNativeACPFixture(t *testing.T, ctx context.Context, binary, adapter, path, proof, directory string, argv, environment []string, metadata map[string]any) {
	t.Helper()
	command := exec.CommandContext(ctx, binary, append([]string{"agent-hooks", "proxy", adapter, path, proof, "--"}, argv...)...)
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
	defer func() { _ = stderr.Close() }()
	command.Stderr = stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = stdin.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}()
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 64<<20)
	rpc := func(id int, method string, params any) json.RawMessage {
		t.Helper()
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
		if _, err := stdin.Write(append(data, '\n')); err != nil {
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
				t.Fatal("invalid ACP response")
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
				reply, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": message.ID, "result": map[string]any{"outcome": map[string]string{"outcome": "selected", "optionId": option}}})
				_, _ = stdin.Write(append(reply, '\n'))
				continue
			}
			if string(message.ID) != fmt.Sprint(id) {
				continue
			}
			if len(message.Error) != 0 {
				t.Fatalf("ACP error: %s", message.Error)
			}
			return message.Result
		}
		stderrData, _ := os.ReadFile(stderrPath)
		t.Fatalf("ACP stream ended: %v: %s", scanner.Err(), stderrData)
		return nil
	}
	_ = rpc(1, "initialize", map[string]any{"protocolVersion": 1, "clientCapabilities": map[string]any{}, "clientInfo": map[string]string{"name": "fixture", "version": "1"}})
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
func encodeFixture(value any) json.RawMessage { data, _ := json.Marshal(value); return data }
