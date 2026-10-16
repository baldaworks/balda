package proxyruntime_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/claude"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/codex"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/opencode"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative/proxyruntime"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/normahq/runtime/v2/agentconfig"
)

func TestMain(m *testing.M) {
	if handled, code := proxyruntime.RunCommandMode(os.Args[1:], claude.Protocol{}, codex.Protocol{}, opencode.Protocol{}); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type runnerFunc func(context.Context, hookcmd.Event) (hookcmd.Result, error)

func (f runnerFunc) Run(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	return f(ctx, event)
}

type host struct {
	service    *hookprovider.Service
	executable string
}

func (h host) Bind(ctx context.Context, scope hookprovider.BindingScope) (*hookprovider.Binding, error) {
	return h.service.Bind(ctx, scope)
}
func (h host) Executable() string { return h.executable }

func TestPreparedProxyCommandPerformsACPHandshakeAndRevokes(t *testing.T) {
	if !hookprovider.NativePlatformSupported() {
		t.Skip("native process ownership requires unix")
	}
	service, err := hookprovider.New(runnerFunc(func(context.Context, hookcmd.Event) (hookcmd.Result, error) { return hookcmd.Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	adapter := claude.New(host{service: service, executable: executable})
	config := agentconfig.Config{Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{executable, "-test.run=^TestACPHelper$", "--", "acp-helper"}}}
	prepared, err := adapter.Prepare(t.Context(), hooknative.LeafRequest{SessionID: "session", ProviderID: "provider", LeafID: "provider", CWD: t.TempDir(), Config: config})
	if err != nil {
		t.Fatal(err)
	}
	run := func() error {
		command := exec.CommandContext(t.Context(), prepared.Config.GenericACP.Cmd[0], prepared.Config.GenericACP.Cmd[1:]...)
		stdin, err := command.StdinPipe()
		if err != nil {
			return err
		}
		stdout, err := command.StdoutPipe()
		if err != nil {
			return err
		}
		if err := command.Start(); err != nil {
			return err
		}
		for _, line := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, `{"jsonrpc":"2.0","id":2,"method":"session/new","params":{"cwd":"/tmp","_meta":{"claudeCode":{"options":{}}}}}`} {
			if _, err := fmt.Fprintln(stdin, line); err != nil {
				return err
			}
		}
		scanner := bufio.NewScanner(stdout)
		count := 0
		for count < 2 && scanner.Scan() {
			count++
		}
		_ = stdin.Close()
		if err := command.Wait(); err != nil {
			return err
		}
		if count != 2 {
			return fmt.Errorf("responses=%d", count)
		}
		return scanner.Err()
	}
	if err := run(); err != nil {
		t.Fatal(err)
	}
	forged := append([]string(nil), prepared.Config.GenericACP.Cmd...)
	forged[5] = strings.Repeat("0", 64)
	if err := exec.CommandContext(t.Context(), forged[0], forged[1:]...).Run(); err == nil {
		t.Fatal("forged owner proof started proxy")
	}
	if err := prepared.Release(); err != nil {
		t.Fatal(err)
	}
	if err := run(); err == nil {
		t.Fatal("revoked prepared command succeeded")
	}
}

func TestACPHelper(t *testing.T) {
	if !slicesContains(os.Args, "acp-helper") {
		return
	}
	binding, err := hookprovider.ReadBinding(os.Getenv(proxyruntime.BindingEnv))
	if err != nil {
		os.Exit(3)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var request struct {
			ID     int    `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(scanner.Bytes(), &request) != nil {
			os.Exit(3)
		}
		if strings.HasPrefix(request.Method, "session/") {
			if err := binding.SignalReady(context.Background()); err != nil {
				os.Exit(3)
			}
		}
		result := map[string]any{}
		if request.Method == "initialize" {
			result["agentInfo"] = map[string]string{"name": "fixture", "version": "0.16.2"}
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result}); err != nil {
			os.Exit(3)
		}
	}
	os.Exit(0)
}

func TestReleaseCancelsUnlimitedDispatch(t *testing.T) {
	started := make(chan struct{})
	service, err := hookprovider.New(runnerFunc(func(ctx context.Context, _ hookcmd.Event) (hookcmd.Result, error) {
		close(started)
		<-ctx.Done()
		return hookcmd.Result{}, ctx.Err()
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	binding, err := service.Bind(t.Context(), hookprovider.BindingScope{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMetadata(binding, "claude"); err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := binding.Invoke(context.Background(), hookcmd.Event{Name: hookcmd.PreToolUse})
		finished <- err
	}()
	<-started
	if err := binding.Release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("release error=%v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unlimited hook survived release")
	}
}

func TestProxyCrashReleaseKillsDescendant(t *testing.T) {
	if !hookprovider.NativePlatformSupported() {
		t.Skip("native process ownership requires unix")
	}
	service, err := hookprovider.New(runnerFunc(func(context.Context, hookcmd.Event) (hookcmd.Result, error) { return hookcmd.Result{}, nil }))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = service.Close() }()
	binding, err := service.Bind(t.Context(), hookprovider.BindingScope{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMetadata(binding, "claude"); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "agent-hooks", "proxy", "claude", binding.Reference(), binding.OwnerProof(), "--", executable, "-test.run=^TestLingeringACPHelper$", "--", "lingering-acp-helper")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
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
	_, _ = fmt.Fprintln(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	var response struct {
		Result struct {
			PID int `json:"nativePid"`
		} `json:"result"`
	}
	if err := json.NewDecoder(stdout).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Result.PID <= 1 {
		t.Fatal("native child did not start")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !hookprovider.ProcessAlive(response.Result.PID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned descendant survived proxy crash")
}

func TestLingeringACPHelper(t *testing.T) {
	if !slicesContains(os.Args, "lingering-acp-helper") {
		return
	}
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request struct {
			ID json.RawMessage `json:"id"`
		}
		_ = json.Unmarshal(scanner.Bytes(), &request)
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": map[string]any{"agentInfo": map[string]string{"name": "fixture", "version": "0.16.2"}, "nativePid": os.Getpid()}})
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

func slicesContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func writeMetadata(binding *hookprovider.Binding, adapter string) error {
	if err := os.WriteFile(binding.Reference()+".adapter", []byte(adapter), 0600); err != nil {
		return err
	}
	return os.WriteFile(binding.Reference()+".owner-proof", []byte(binding.OwnerProof()), 0600)
}
