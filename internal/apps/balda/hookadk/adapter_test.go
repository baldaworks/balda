package hookadk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
	"github.com/baldaworks/balda/internal/apps/balda/hooks"
	"google.golang.org/adk/v2/agent"
	"google.golang.org/adk/v2/agent/llmagent"
	"google.golang.org/adk/v2/model"
	"google.golang.org/adk/v2/plugin"
	"google.golang.org/adk/v2/runner"
	"google.golang.org/adk/v2/session"
	"google.golang.org/adk/v2/tool"
	"google.golang.org/adk/v2/tool/functiontool"
	"google.golang.org/genai"
)

type testArgs struct {
	Value int `json:"value"`
}

type testResult struct {
	Value int `json:"value"`
}

type recordingRunner struct {
	mu       sync.Mutex
	events   []hookcmd.Event
	run      func(hookcmd.Event) (hookcmd.Result, error)
	released []string
}

func (r *recordingRunner) Run(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
	r.mu.Lock()
	r.events = append(r.events, event)
	run := r.run
	r.mu.Unlock()
	if run == nil {
		return hookcmd.Result{}, nil
	}
	return run(event)
}

func (r *recordingRunner) ReleaseInvocation(sessionID, invocationID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.released = append(r.released, sessionID+"/"+invocationID)
}

func (r *recordingRunner) snapshot() ([]hookcmd.Event, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]hookcmd.Event(nil), r.events...), append([]string(nil), r.released...)
}

type scriptedModel struct {
	mu    sync.Mutex
	calls int
}

func (*scriptedModel) Name() string { return "hook-adapter-test" }

func (m *scriptedModel) GenerateContent(_ context.Context, _ *model.LLMRequest, _ bool) iter.Seq2[*model.LLMResponse, error] {
	m.mu.Lock()
	call := m.calls
	m.calls++
	callID := fmt.Sprintf("call-%d", call/2+1)
	m.mu.Unlock()
	return func(yield func(*model.LLMResponse, error) bool) {
		if call%2 == 0 {
			content := genai.NewContentFromFunctionCall("arbitrary_tool", map[string]any{"value": float64(3)}, genai.RoleModel)
			content.Parts[0].FunctionCall.ID = callID
			yield(&model.LLMResponse{Content: content}, nil)
			return
		}
		yield(&model.LLMResponse{Content: genai.NewContentFromText("done", genai.RoleModel)}, nil)
	}
}

func TestAdapterFunctionToolOutcomes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		hook          func(hookcmd.Event) (hookcmd.Result, error)
		toolError     bool
		wantToolCalls int
		wantToolValue int
		wantResponse  map[string]any
	}{
		{name: "allow", wantToolCalls: 1, wantToolValue: 3, wantResponse: map[string]any{"value": float64(3)}},
		{
			name: "rewrite input",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PreToolUse {
					return hookcmd.Result{UpdatedInput: json.RawMessage(`{"value":7}`)}, nil
				}
				return hookcmd.Result{}, nil
			},
			wantToolCalls: 1,
			wantToolValue: 7,
			wantResponse:  map[string]any{"value": float64(7)},
		},
		{
			name: "deny before execution",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PreToolUse {
					return hookcmd.Result{}, &hookcmd.DeniedError{Hook: "managed/policy", Reason: "not allowed"}
				}
				return hookcmd.Result{UpdatedOutput: json.RawMessage(`{"masked":true}`)}, nil
			},
			wantResponse: errorResponse(deniedCode, "not allowed"),
		},
		{
			name: "fail closed before execution",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PreToolUse {
					return hookcmd.Result{}, hookcmd.ErrExecution
				}
				return hookcmd.Result{UpdatedOutput: json.RawMessage(`{"masked":true}`)}, nil
			},
			wantResponse: errorResponse(unavailableCode, "Tool call blocked because hook policy was unavailable."),
		},
		{
			name: "replace result",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PostToolUse {
					return hookcmd.Result{UpdatedOutput: json.RawMessage(`{"value":99}`)}, nil
				}
				return hookcmd.Result{}, nil
			},
			wantToolCalls: 1,
			wantToolValue: 3,
			wantResponse:  map[string]any{"value": float64(99)},
		},
		{
			name: "post denial does not claim prevention",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PostToolUse {
					return hookcmd.Result{}, &hookcmd.DeniedError{Hook: "managed/policy", Reason: "reject output"}
				}
				return hookcmd.Result{}, nil
			},
			wantToolCalls: 1,
			wantToolValue: 3,
			wantResponse:  errorResponse(postFailureCode, "Tool completed, but post-tool policy rejected its response."),
		},
		{
			name:          "normalize tool error",
			toolError:     true,
			wantToolCalls: 1,
			wantToolValue: 3,
			wantResponse:  errorResponse(toolFailureCode, "fixture failure"),
		},
		{
			name: "attach untrusted context as result data",
			hook: func(event hookcmd.Event) (hookcmd.Result, error) {
				if event.Name == hookcmd.PostToolUse {
					return hookcmd.Result{AdditionalContext: "untrusted note"}, nil
				}
				return hookcmd.Result{}, nil
			},
			wantToolCalls: 1,
			wantToolValue: 3,
			wantResponse:  map[string]any{"value": float64(3), hookContextField: "untrusted note"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			hooksRunner := &recordingRunner{run: tt.hook}
			var toolCalls atomic.Int32
			var toolValue atomic.Int32
			handler := func(_ agent.Context, args testArgs) (testResult, error) {
				toolCalls.Add(1)
				toolValue.Store(int32(args.Value))
				if tt.toolError {
					return testResult{}, errors.New("fixture failure")
				}
				return testResult(args), nil
			}
			got := runFunctionTool(t, hooksRunner, handler)
			if gotCalls := int(toolCalls.Load()); gotCalls != tt.wantToolCalls {
				t.Fatalf("tool calls = %d, want %d", gotCalls, tt.wantToolCalls)
			}
			if gotValue := int(toolValue.Load()); gotValue != tt.wantToolValue {
				t.Errorf("tool input value = %d, want %d", gotValue, tt.wantToolValue)
			}
			if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, tt.wantResponse); gotJSON != wantJSON {
				t.Errorf("function response = %s, want %s", gotJSON, wantJSON)
			}
			events, released := hooksRunner.snapshot()
			if len(events) == 0 || events[0].Source != sourceADK || events[0].Provider != "opaque-provider" || events[0].SessionID != "session" || events[0].AgentSessionID != "session" || events[0].InvocationID == "" || events[0].ToolCallID == "" {
				t.Errorf("normalized event = %#v", events)
			}
			if tt.wantToolCalls == 0 && (len(events) != 1 || events[0].Name != hookcmd.PreToolUse) {
				t.Errorf("short-circuited hook events = %#v, want PreToolUse only", events)
			}
			if len(released) != 1 {
				t.Errorf("released invocations = %v, want one", released)
			}
		})
	}
}

func TestAdapterCoalescesToolErrorAndAfterCallbacks(t *testing.T) {
	t.Parallel()

	reader := &countingSnapshotReader{}
	runner := hooks.NewRunner(reader)
	runFunctionTool(t, runner, func(_ agent.Context, _ testArgs) (testResult, error) {
		return testResult{}, errors.New("fixture failure")
	})
	if got := reader.loads.Load(); got != 2 {
		t.Fatalf("execution snapshots loaded = %d, want pre and one coalesced post", got)
	}
}

func TestAdapterCallbackOrderingAndTrustedExtensions(t *testing.T) {
	t.Parallel()

	t.Run("pre denial stops trusted downstream callbacks and cannot be masked", func(t *testing.T) {
		t.Parallel()
		hooksRunner := &recordingRunner{run: func(event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name == hookcmd.PreToolUse {
				return hookcmd.Result{}, &hookcmd.DeniedError{Hook: "managed/policy", Reason: "blocked"}
			}
			return hookcmd.Result{}, nil
		}}
		var callerBefore, callerAfter atomic.Bool
		caller := mustCallerPlugin(t, plugin.Config{
			Name: "caller",
			BeforeToolCallback: func(agent.Context, tool.Tool, map[string]any) (map[string]any, error) {
				callerBefore.Store(true)
				return map[string]any{"owner": "caller-before"}, nil
			},
			AfterToolCallback: func(agent.Context, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
				callerAfter.Store(true)
				return map[string]any{"owner": "caller-after"}, nil
			},
		})
		got := runFunctionToolWithCallerPlugins(t, hooksRunner, func(_ agent.Context, args testArgs) (testResult, error) {
			return testResult(args), nil
		}, caller)
		if callerBefore.Load() || callerAfter.Load() {
			t.Fatalf("caller callbacks ran: before=%v after=%v", callerBefore.Load(), callerAfter.Load())
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, errorResponse(deniedCode, "blocked")); gotJSON != wantJSON {
			t.Fatalf("response = %s, want %s", gotJSON, wantJSON)
		}
	})

	t.Run("trusted downstream callback may transform accepted arguments", func(t *testing.T) {
		t.Parallel()
		var observedModelInput atomic.Int32
		hooksRunner := &recordingRunner{run: func(event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name != hookcmd.PreToolUse {
				return hookcmd.Result{}, nil
			}
			var input testArgs
			if err := json.Unmarshal(event.ToolInput, &input); err != nil {
				return hookcmd.Result{}, err
			}
			observedModelInput.Store(int32(input.Value))
			return hookcmd.Result{UpdatedInput: json.RawMessage(`{"value":7}`)}, nil
		}}
		var callerInput atomic.Int32
		caller := mustCallerPlugin(t, plugin.Config{
			Name: "trusted-caller",
			BeforeToolCallback: func(_ agent.Context, _ tool.Tool, args map[string]any) (map[string]any, error) {
				callerInput.Store(int32(args["value"].(float64)))
				args["value"] = float64(11)
				return nil, nil
			},
		})
		got := runFunctionToolWithCallerPlugins(t, hooksRunner, func(_ agent.Context, args testArgs) (testResult, error) {
			return testResult(args), nil
		}, caller)
		if observedModelInput.Load() != 3 || callerInput.Load() != 7 {
			t.Fatalf("inputs: Balda=%d trusted caller=%d, want 3 then 7", observedModelInput.Load(), callerInput.Load())
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, map[string]any{"value": float64(11)}); gotJSON != wantJSON {
			t.Fatalf("response = %s, want trusted downstream transformation %s", gotJSON, wantJSON)
		}
	})

	t.Run("post replacement precedes caller after callback", func(t *testing.T) {
		t.Parallel()
		hooksRunner := &recordingRunner{run: func(event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name == hookcmd.PostToolUse {
				return hookcmd.Result{UpdatedOutput: json.RawMessage(`{"owner":"balda"}`)}, nil
			}
			return hookcmd.Result{}, nil
		}}
		var callerBefore, callerAfter atomic.Bool
		caller := mustCallerPlugin(t, plugin.Config{
			Name: "caller",
			BeforeToolCallback: func(agent.Context, tool.Tool, map[string]any) (map[string]any, error) {
				callerBefore.Store(true)
				return nil, nil
			},
			AfterToolCallback: func(agent.Context, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
				callerAfter.Store(true)
				return map[string]any{"owner": "caller"}, nil
			},
		})
		got := runFunctionToolWithCallerPlugins(t, hooksRunner, func(_ agent.Context, args testArgs) (testResult, error) {
			return testResult(args), nil
		}, caller)
		if !callerBefore.Load() || callerAfter.Load() {
			t.Fatalf("caller callbacks: before=%v after=%v", callerBefore.Load(), callerAfter.Load())
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, map[string]any{"owner": "balda"}); gotJSON != wantJSON {
			t.Fatalf("response = %s, want %s", gotJSON, wantJSON)
		}
	})

	t.Run("error replacement precedes caller error callback", func(t *testing.T) {
		t.Parallel()
		hooksRunner := &recordingRunner{run: func(event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name == hookcmd.PostToolUse {
				return hookcmd.Result{UpdatedOutput: json.RawMessage(`{"owner":"balda"}`)}, nil
			}
			return hookcmd.Result{}, nil
		}}
		var callerError atomic.Bool
		caller := mustCallerPlugin(t, plugin.Config{
			Name: "caller",
			OnToolErrorCallback: func(agent.Context, tool.Tool, map[string]any, error) (map[string]any, error) {
				callerError.Store(true)
				return map[string]any{"owner": "caller"}, nil
			},
		})
		got := runFunctionToolWithCallerPlugins(t, hooksRunner, func(agent.Context, testArgs) (testResult, error) {
			return testResult{}, errors.New("fixture failure")
		}, caller)
		if callerError.Load() {
			t.Fatal("caller error callback ran after actionable Balda policy")
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, map[string]any{"owner": "balda"}); gotJSON != wantJSON {
			t.Fatalf("response = %s, want %s", gotJSON, wantJSON)
		}
	})

	t.Run("unchanged policy continues caller callbacks", func(t *testing.T) {
		t.Parallel()
		hooksRunner := &recordingRunner{}
		var callerBefore, callerError, callerAfter atomic.Bool
		caller := mustCallerPlugin(t, plugin.Config{
			Name: "caller",
			BeforeToolCallback: func(agent.Context, tool.Tool, map[string]any) (map[string]any, error) {
				callerBefore.Store(true)
				return nil, nil
			},
			OnToolErrorCallback: func(agent.Context, tool.Tool, map[string]any, error) (map[string]any, error) {
				callerError.Store(true)
				return map[string]any{"owner": "caller-error"}, nil
			},
			AfterToolCallback: func(agent.Context, tool.Tool, map[string]any, map[string]any, error) (map[string]any, error) {
				callerAfter.Store(true)
				return map[string]any{"owner": "caller-after"}, nil
			},
		})
		got := runFunctionToolWithCallerPlugins(t, hooksRunner, func(agent.Context, testArgs) (testResult, error) {
			return testResult{}, errors.New("fixture failure")
		}, caller)
		if !callerBefore.Load() || !callerError.Load() || !callerAfter.Load() {
			t.Fatalf("caller callbacks: before=%v error=%v after=%v", callerBefore.Load(), callerError.Load(), callerAfter.Load())
		}
		if gotJSON, wantJSON := mustJSON(t, got), mustJSON(t, map[string]any{"owner": "caller-after"}); gotJSON != wantJSON {
			t.Fatalf("response = %s, want %s", gotJSON, wantJSON)
		}
	})
}

func TestAdapterNoToolAgentEmitsNoHookEvents(t *testing.T) {
	t.Parallel()

	hooksRunner := &recordingRunner{}
	p := mustPlugin(t, hooksRunner)
	custom, err := agent.New(agent.Config{
		Name: "custom",
		Run: func(agent.InvocationContext) iter.Seq2[*session.Event, error] {
			return func(func(*session.Event, error) bool) {}
		},
	})
	if err != nil {
		t.Fatalf("agent.New() error = %v", err)
	}
	r := mustRunner(t, custom, p)
	collectRun(t, r)
	events, released := hooksRunner.snapshot()
	if len(events) != 0 {
		t.Errorf("hook events = %#v, want none", events)
	}
	if len(released) != 1 {
		t.Errorf("released invocations = %v, want one", released)
	}
}

func TestAdapterUsesLiveManagedDefinitionsOnNextEvent(t *testing.T) {
	t.Setenv("GO_WANT_HOOK_ADK_HELPER", "1")

	reader := &liveSnapshotReader{mode: "rewrite-4", command: os.Args[0]}
	hooksRunner := hooks.NewRunner(reader)
	var mu sync.Mutex
	var values []int
	handler := func(_ agent.Context, args testArgs) (testResult, error) {
		mu.Lock()
		values = append(values, args.Value)
		mu.Unlock()
		return testResult(args), nil
	}
	functionTool, err := functiontool.New(functiontool.Config{Name: "arbitrary_tool"}, handler)
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	llm, err := llmagent.New(llmagent.Config{Name: "test-agent", Model: &scriptedModel{}, Tools: []tool.Tool{functionTool}})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	r := mustRunner(t, llm, mustPlugin(t, hooksRunner))
	collectRun(t, r)
	reader.setMode("rewrite-8")
	collectRun(t, r)

	mu.Lock()
	defer mu.Unlock()
	if got, want := fmt.Sprint(values), "[4 8]"; got != want {
		t.Fatalf("tool inputs = %s, want %s", got, want)
	}
}

func TestAdapterBoundsStructuredFailures(t *testing.T) {
	t.Parallel()

	hooksRunner := &recordingRunner{run: func(event hookcmd.Event) (hookcmd.Result, error) {
		if event.Name == hookcmd.PreToolUse {
			return hookcmd.Result{}, &hookcmd.DeniedError{Hook: "managed/policy", Reason: string(make([]byte, maxMessageBytes*2))}
		}
		return hookcmd.Result{}, nil
	}}
	response := runFunctionTool(t, hooksRunner, func(_ agent.Context, args testArgs) (testResult, error) {
		return testResult(args), nil
	})
	errorValue, ok := response["error"].(map[string]any)
	if !ok {
		t.Fatalf("response error = %#v, want structured object", response["error"])
	}
	message, ok := errorValue["message"].(string)
	if !ok || len(message) > maxMessageBytes {
		t.Fatalf("bounded message length = %d, want at most %d", len(message), maxMessageBytes)
	}
}

func TestAdapterRejectsNonAbsoluteWorkingDirectory(t *testing.T) {
	t.Parallel()

	_, err := NewAdapter(&recordingRunner{}).NewPlugin(Scope{CWD: "relative"})
	if err == nil {
		t.Fatal("NewPlugin() error = nil, want invalid working directory")
	}
}

// TestHookADKHelper is a subprocess entrypoint used by the live-definition
// integration test.
func TestHookADKHelper(t *testing.T) {
	if os.Getenv("GO_WANT_HOOK_ADK_HELPER") != "1" {
		return
	}
	mode := os.Args[len(os.Args)-1]
	var value int
	switch mode {
	case "rewrite-4":
		value = 4
	case "rewrite-8":
		value = 8
	default:
		os.Exit(1)
	}
	_, _ = fmt.Fprintf(os.Stdout, `{"updated_input":{"value":%d}}`, value)
	os.Exit(0)
}

func runFunctionTool(t *testing.T, hooksRunner Runner, handler func(agent.Context, testArgs) (testResult, error)) map[string]any {
	t.Helper()
	return runFunctionToolWithCallerPlugins(t, hooksRunner, handler)
}

func runFunctionToolWithCallerPlugins(t *testing.T, hooksRunner Runner, handler func(agent.Context, testArgs) (testResult, error), callers ...*plugin.Plugin) map[string]any {
	t.Helper()
	functionTool, err := functiontool.New(functiontool.Config{Name: "arbitrary_tool"}, handler)
	if err != nil {
		t.Fatalf("functiontool.New() error = %v", err)
	}
	llm, err := llmagent.New(llmagent.Config{
		Name:  "test-agent",
		Model: &scriptedModel{},
		Tools: []tool.Tool{functionTool},
	})
	if err != nil {
		t.Fatalf("llmagent.New() error = %v", err)
	}
	plugins := append([]*plugin.Plugin{mustPlugin(t, hooksRunner)}, callers...)
	r := mustRunner(t, llm, plugins...)
	return collectRun(t, r)
}

func mustCallerPlugin(t *testing.T, cfg plugin.Config) *plugin.Plugin {
	t.Helper()
	p, err := plugin.New(cfg)
	if err != nil {
		t.Fatalf("plugin.New() error = %v", err)
	}
	return p
}

func mustPlugin(t *testing.T, hooksRunner Runner) *plugin.Plugin {
	t.Helper()
	p, err := NewAdapter(hooksRunner).NewPlugin(Scope{Provider: "opaque-provider", CWD: t.TempDir()})
	if err != nil {
		t.Fatalf("NewPlugin() error = %v", err)
	}
	return p
}

func mustRunner(t *testing.T, a agent.Agent, plugins ...*plugin.Plugin) *runner.Runner {
	t.Helper()
	sessions := session.InMemoryService()
	if _, err := sessions.Create(t.Context(), &session.CreateRequest{AppName: "app", UserID: "user", SessionID: "session"}); err != nil {
		t.Fatalf("session.Create() error = %v", err)
	}
	r, err := runner.New(runner.Config{
		AppName:        "app",
		Agent:          a,
		SessionService: sessions,
		PluginConfig:   runner.PluginConfig{Plugins: plugins},
	})
	if err != nil {
		t.Fatalf("runner.New() error = %v", err)
	}
	return r
}

func collectRun(t *testing.T, r *runner.Runner) map[string]any {
	t.Helper()
	var response map[string]any
	for event, err := range r.Run(t.Context(), "user", "session", genai.NewContentFromText("run", genai.RoleUser), agent.RunConfig{}) {
		if err != nil {
			t.Fatalf("runner.Run() error = %v", err)
		}
		if event == nil || event.Content == nil {
			continue
		}
		for _, part := range event.Content.Parts {
			if part.FunctionResponse != nil {
				response = part.FunctionResponse.Response
			}
		}
	}
	return response
}

type countingSnapshotReader struct {
	loads atomic.Int32
}

type liveSnapshotReader struct {
	mu      sync.Mutex
	mode    string
	command string
}

func (r *liveSnapshotReader) LoadExecutionSnapshot(context.Context) (hookcmd.ExecutionSnapshot, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{{
		ID: "managed/live",
		Definition: hookcmd.Definition{
			Name:    "live",
			Event:   hookcmd.PreToolUse,
			Enabled: true,
			Command: r.command,
			Args:    []string{"-test.run=^TestHookADKHelper$", "--", r.mode},
		},
	}}}, nil
}

func (r *liveSnapshotReader) setMode(mode string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mode = mode
}

func (r *countingSnapshotReader) LoadExecutionSnapshot(context.Context) (hookcmd.ExecutionSnapshot, error) {
	r.loads.Add(1)
	return hookcmd.ExecutionSnapshot{}, nil
}

func errorResponse(code, message string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "message": message}}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%#v) error = %v", value, err)
	}
	return string(data)
}
