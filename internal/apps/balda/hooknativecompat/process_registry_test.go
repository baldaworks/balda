package hooknativecompat

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
)

func TestPrivateProxyHelper(t *testing.T) {
	index := slices.Index(os.Args, "private-proxy-helper")
	if index < 0 {
		return
	}
	_, code := RunCommandMode(os.Args[index+1:])
	os.Exit(code)
}

func TestLingeringACPHelper(t *testing.T) {
	if !slices.Contains(os.Args, "lingering-acp-helper") {
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
	// Simulate a native backend that remains alive after its proxy is killed.
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestNativeProxyCrashReleasesOwnedProcessGroup(t *testing.T) {
	if !hookprovider.NativePlatformSupported() {
		t.Skip("native adapters require Linux or macOS")
	}
	service := testService(t, hookcmd.Definition{})
	path, release, err := service.bind(kindClaude, hookcmd.Event{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = release() })
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	proof, err := readOwnerProof(path)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestPrivateProxyHelper$", "--", "private-proxy-helper", "agent-hooks", "proxy", path, proof, "--", executable, "-test.run=^TestLingeringACPHelper$", "--", "lingering-acp-helper")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = io.Discard
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
	_, err = io.WriteString(stdin, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`+"\n")
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Result struct {
			PID int `json:"nativePid"`
		} `json:"result"`
	}
	if err := json.NewDecoder(stdout).Decode(&response); err != nil {
		t.Fatal(err)
	}
	if response.Result.PID <= 1 {
		t.Fatal("native child was not started")
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !hookprovider.ProcessAlive(response.Result.PID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("owned native process survived proxy crash and session release")
}
