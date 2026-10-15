package hooknativecompat

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooknative"
	"github.com/baldaworks/balda/internal/apps/balda/hookprovider"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"github.com/normahq/runtime/v2/agentconfig"
)

const (
	modeRewrite = "rewrite"
	modePost    = "post"
)

type configurationFixture struct{ record hookcmd.Record }

func (f configurationFixture) Load(context.Context) (hookcmd.Record, error) { return f.record, nil }

func nativeDefinition(t *testing.T, mode string) hookcmd.Definition {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return hookcmd.Definition{Name: "guard", Enabled: true, Event: hookcmd.PreToolUse, Command: executable, Args: []string{"-test.run=^TestNativeCommandHelper$", "--", "balda-native-helper", mode}, TimeoutSeconds: 5}
}

func TestNativeActivationRequiresSessionBinding(t *testing.T) {
	if !hookprovider.NativePlatformSupported() {
		t.Skip("native session activation requires Linux or macOS")
	}
	service := testService(t, nativeDefinition(t, "inspect"))
	if err := service.VerifySession(t.Context(), "session"); !errors.Is(err, hookcmd.ErrSessionReset) {
		t.Fatalf("unbound session = %v", err)
	}
	_, release, err := prepareProvider(t, service, "session", t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySession(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	if err := service.VerifySession(t.Context(), "session"); !errors.Is(err, hookcmd.ErrSessionReset) {
		t.Fatalf("released session = %v", err)
	}
}

func TestDisabledNativeHooksPreserveProviderLaunch(t *testing.T) {
	service := testService(t, hookcmd.Definition{})
	required, err := service.Required(t.Context(), "provider")
	if err != nil {
		t.Fatal(err)
	}
	if required {
		t.Fatal("disabled native hooks requested launch preparation")
	}
	if err := service.VerifySession(t.Context(), "session"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeCommandHelper(t *testing.T) {
	index := slices.Index(os.Args, "balda-native-helper")
	if index < 0 {
		return
	}
	data, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(3)
	}
	var event hookcmd.Event
	if json.Unmarshal(data, &event) != nil {
		os.Exit(3)
	}
	var result hookcmd.Result
	if len(os.Args) > index+2 {
		seconds, err := strconv.Atoi(os.Args[index+2])
		if err != nil || seconds < 0 {
			os.Exit(3)
		}
		time.Sleep(time.Duration(seconds) * time.Second)
	}
	switch os.Args[index+1] {
	case "inspect":
		result.AdditionalContext = string(data)
	case modeRewrite:
		result.UpdatedInput = json.RawMessage(`{"command":"approved command"}`)
	case hookcmd.DecisionDeny:
		result.Decision, result.Reason = hookcmd.DecisionDeny, "guard rejected this call"
	case modePost:
		result.AdditionalContext = "checked tool result"
	case "large-context":
		result.AdditionalContext = strings.Repeat("<", 32<<10)
	case "large-rewrite":
		result.UpdatedInput = encode(map[string]string{"command": strings.Repeat("x", 50<<10)})
	case "wait":
		if os.WriteFile(filepath.Join(event.CWD, "hook-started"), nil, 0600) != nil {
			os.Exit(3)
		}
		time.Sleep(30 * time.Second)
	default:
		os.Exit(3)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(result) != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestBridgePreservesCombinedContextAndInputRewrite(t *testing.T) {
	contextHook := nativeDefinition(t, "large-context")
	contextHook.Name = "a-context"
	rewriteHook := nativeDefinition(t, "large-rewrite")
	rewriteHook.Name = "b-rewrite"
	store := configurationFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{contextHook, rewriteHook}}}
	service := testServiceWithStore(t, store)
	path, release, err := service.bind(kindCodex, hookcmd.Event{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	input := []byte(`{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{}}`)
	var output bytes.Buffer
	if err := Dispatch(t.Context(), path, bytes.NewReader(input), &output); err != nil {
		t.Fatal(err)
	}
	var envelope struct {
		Specific struct {
			Context string            `json:"additionalContext"`
			Input   map[string]string `json:"updatedInput"`
		} `json:"hookSpecificOutput"`
	}
	if json.Unmarshal(output.Bytes(), &envelope) != nil || envelope.Specific.Context != strings.Repeat("<", 32<<10) || envelope.Specific.Input["command"] != strings.Repeat("x", 50<<10) {
		t.Fatal("valid combined hook results were truncated or rejected by the private bridge")
	}
}

func TestSessionReleaseCancelsUnlimitedNativeHook(t *testing.T) {
	definition := nativeDefinition(t, "wait")
	definition.TimeoutSeconds = 0
	service := testService(t, definition)
	cwd := t.TempDir()
	path, release, err := service.bind(kindCodex, hookcmd.Event{SessionID: "session", Provider: "provider", CWD: cwd})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = release() }()
	binding, err := readBinding(path)
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan error, 1)
	go func() {
		_, err := invoke(t.Context(), binding, hookcmd.Event{Name: hookcmd.PreToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{}`)})
		finished <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(cwd, "hook-started")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("native hook did not start")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := release(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("released invocation = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("unlimited native hook survived session release")
	}
}

func testService(t *testing.T, definition hookcmd.Definition) *Service {
	t.Helper()
	record := hookcmd.Record{}
	if definition.Name != "" {
		record = hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}
	}
	store := configurationFixture{record: record}
	return testServiceWithStore(t, store)
}

func testServiceWithStore(t *testing.T, store configurationFixture) *Service {
	t.Helper()
	providers := map[string]agentconfig.Config{"provider": {Type: agentconfig.AgentTypeClaudeCodeACP, ClaudeCodeACP: &agentconfig.ACPConfig{Cmd: []string{"fixture-acp"}, Model: "model", Mode: "default"}}}
	service, err := New(hooks.NewRunner(store), store, "provider", providers)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := service.Close(); err != nil {
			t.Error(err)
		}
	})
	return service
}

func prepareProvider(t testing.TB, service *Service, sessionID, cwd string) (map[string]agentconfig.Config, func() error, error) {
	t.Helper()
	prepared, err := service.Prepare(t.Context(), hooknative.LeafRequest{
		SessionID:  sessionID,
		ProviderID: "provider",
		LeafID:     "provider",
		CWD:        cwd,
		Config:     service.providers["provider"],
	})
	if err != nil {
		return nil, nil, err
	}
	providers := maps.Clone(service.providers)
	providers["provider"] = prepared.Config
	return providers, prepared.Release, nil
}

func TestBridgeBindsTrustedSessionContextAndReleasesCapabilities(t *testing.T) {
	if !hookprovider.NativePlatformSupported() {
		t.Skip("native launch preparation requires Linux or macOS")
	}
	service := testService(t, nativeDefinition(t, "inspect"))
	for _, sessionID := range []string{"session-a", "session-b"} {
		cwd := t.TempDir()
		prepared, release, err := prepareProvider(t, service, sessionID, cwd)
		if err != nil {
			t.Fatal(err)
		}
		command := prepared["provider"].GenericACP.Cmd
		path := command[3]
		binding, err := readBinding(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("private binding permissions: %v %v", info, err)
		}
		result, err := invoke(t.Context(), binding, hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "spoofed", Provider: "spoofed", CWD: "/spoofed", ToolName: "Bash", ToolInput: json.RawMessage(`{"command":"literal $(touch unexpected)"}`)})
		if err != nil {
			t.Fatal(err)
		}
		var observed hookcmd.Event
		if json.Unmarshal([]byte(result.AdditionalContext), &observed) != nil || observed.SessionID != sessionID || observed.Provider != "provider" || observed.CWD != cwd {
			t.Fatalf("trusted context: %+v", observed)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if err := release(); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("binding directory survived release: %v", err)
		}
		if _, err := invoke(t.Context(), binding, hookcmd.Event{Name: hookcmd.PreToolUse, ToolName: "Bash", ToolInput: json.RawMessage(`{}`)}); !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("revoked binding invocation: %v", err)
		}
	}
	if service.providers["provider"].Type != agentconfig.AgentTypeClaudeCodeACP || service.providers["provider"].ClaudeCodeACP.Cmd[0] != "fixture-acp" {
		t.Fatal("shared provider configuration mutated")
	}
}

func TestDispatchNativeDecisionsAndRewrites(t *testing.T) {
	for _, kind := range []string{kindClaude, kindCodex} {
		for _, mode := range []string{hookcmd.DecisionDeny, modeRewrite, modePost} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				definition := nativeDefinition(t, mode)
				if mode == modePost {
					definition.Event = hookcmd.PostToolUse
				}
				service := testService(t, definition)
				path, release, err := service.bind(kind, hookcmd.Event{SessionID: "session", Provider: "provider", CWD: t.TempDir()})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { _ = release() })
				input := encode(map[string]any{"hook_event_name": definition.Event, "session_id": "native-session", "tool_name": "Bash", "tool_input": map[string]string{"command": "original"}, "tool_response": map[string]string{"stdout": "result"}})
				var output bytes.Buffer
				if err := Dispatch(t.Context(), path, bytes.NewReader(input), &output); err != nil {
					t.Fatal(err)
				}
				var envelope map[string]any
				if json.Unmarshal(output.Bytes(), &envelope) != nil {
					t.Fatal("invalid native output")
				}
				control := envelope["hookSpecificOutput"].(map[string]any)
				switch mode {
				case hookcmd.DecisionDeny:
					if control["permissionDecision"] != hookcmd.DecisionDeny || control["permissionDecisionReason"] != "guard rejected this call" {
						t.Fatalf("denial: %s", output.String())
					}
				case modeRewrite:
					want := "allow"
					if kind == kindClaude {
						want = "ask"
					}
					if control["permissionDecision"] != want || control["updatedInput"].(map[string]any)["command"] != "approved command" {
						t.Fatalf("rewrite: %s", output.String())
					}
				case modePost:
					if control["additionalContext"] != "checked tool result" {
						t.Fatalf("post context: %s", output.String())
					}
				}
			})
		}
	}
}

func TestNativeCapabilitiesRejectUnsupportedPoolAndMissingProvider(t *testing.T) {
	service := testService(t, hookcmd.Definition{})
	definition := hookcmd.Definition{Enabled: true, Event: hookcmd.PreToolUse}
	if !hookprovider.NativePlatformSupported() {
		if err := service.ValidateDefinition(t.Context(), definition); !errors.Is(err, hookcmd.ErrUnsupported) {
			t.Fatalf("native platform capability = %v", err)
		}
		return
	}
	if err := service.ValidateDefinition(t.Context(), definition); err != nil {
		t.Fatal(err)
	}
	definition.Provider = "missing"
	if err := service.ValidateDefinition(t.Context(), definition); !errors.Is(err, hookcmd.ErrUnsupported) {
		t.Fatalf("unknown provider: %v", err)
	}
	definition.Provider = ""
	service.providers["provider"] = agentconfig.Config{Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"unknown-agent"}}}
	if err := service.ValidateDefinition(t.Context(), definition); !errors.Is(err, hookcmd.ErrUnsupported) {
		t.Fatalf("unsupported provider: %v", err)
	}
	definition.Enabled = false
	if err := service.ValidateDefinition(t.Context(), definition); err != nil {
		t.Fatal("disabled draft must remain editable")
	}
}

func TestClaudeMetadataInjectionPreservesProtocolAndUserSettings(t *testing.T) {
	for _, method := range []string{"session/new", "session/load", "session/resume"} {
		line := []byte(fmt.Sprintf(`{"jsonrpc":"2.0","id":9007199254740993,"method":%q,"params":{"cwd":"/work","mcpServers":[{"name":"server"}],"_meta":{"systemPrompt":"keep instruction","claudeCode":{"options":{"settingSources":["user","project"],"settings":{"permissions":{"deny":["Bash(rm *)"]},"hooks":{"PreToolUse":[{"matcher":"Read","hooks":[]}]}}}}}}}`, method))
		injected, err := injectClaude(line)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(injected), "9007199254740993") || !strings.Contains(string(injected), "keep instruction") || !strings.Contains(string(injected), "Bash(rm *)") || !strings.Contains(string(injected), "settingSources") || !strings.Contains(string(injected), nativeCommand()[:2]) {
			t.Fatalf("metadata lost: %s", injected)
		}
		var message map[string]json.RawMessage
		_ = json.Unmarshal(injected, &message)
		params, _ := object(message["params"])
		meta, _ := object(params["_meta"])
		claude, _ := object(meta["claudeCode"])
		options, _ := object(claude["options"])
		extra, _ := object(options["extraArgs"])
		var inline string
		_ = json.Unmarshal(extra["settings"], &inline)
		settings, _ := object([]byte(inline))
		hooks, _ := object(settings["hooks"])
		var groups []json.RawMessage
		if json.Unmarshal(hooks["PreToolUse"], &groups) != nil || len(groups) != 2 {
			t.Fatal("user hooks overwritten")
		}
	}
	line := []byte(`{"jsonrpc":"2.0","method":"session/prompt","params":{"prompt":[{"type":"text","text":"$(literal)"}]}}`)
	got, err := injectClaude(line)
	if err != nil || !bytes.Equal(got, line) {
		t.Fatal("unrelated ACP request changed")
	}
}
