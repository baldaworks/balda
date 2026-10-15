package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

type recordFixture struct {
	record hookcmd.Record
	err    error
}

type snapshotReaderFunc func(context.Context) (hookcmd.ExecutionSnapshot, error)

func (f snapshotReaderFunc) LoadExecutionSnapshot(ctx context.Context) (hookcmd.ExecutionSnapshot, error) {
	return f(ctx)
}

func (f recordFixture) Load(context.Context) (hookcmd.Record, error) { return f.record, f.err }

func helperDefinition(t *testing.T, name, mode string) hookcmd.Definition {
	t.Helper()
	command, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return hookcmd.Definition{Name: name, Event: hookcmd.UserPromptSubmit, Enabled: true, Command: command, Args: []string{"-test.run=^TestHookCommandHelper$", "--", "balda-hook-helper", mode}, TimeoutSeconds: 3}
}

func TestHookCommandHelper(t *testing.T) {
	index := slices.Index(os.Args, "balda-hook-helper")
	if index < 0 {
		return
	}
	mode := os.Args[index+1]
	switch mode {
	case "context":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(3)
		}
		var event hookcmd.Event
		if json.Unmarshal(data, &event) != nil {
			os.Exit(3)
		}
		data, err = json.Marshal(hookcmd.Result{AdditionalContext: event.Prompt})
		if err != nil {
			os.Exit(3)
		}
		_, _ = os.Stdout.Write(data)
	case hookcmd.DecisionDeny:
		_, _ = fmt.Fprint(os.Stdout, `{"decision":"deny","reason":"policy rejects this request"}`)
	case "exit-deny":
		os.Exit(2)
	case "failure":
		_, _ = fmt.Fprint(os.Stderr, "private-command-canary")
		os.Exit(3)
	case "timeout":
		time.Sleep(5 * time.Second)
	case "oversize":
		_, _ = fmt.Fprint(os.Stdout, strings.Repeat("x", maxOutputBytes+1))
	case "oversize-wait", "stderr-oversize-wait":
		output := os.Stdout
		if mode == "stderr-oversize-wait" {
			output = os.Stderr
		}
		_, _ = fmt.Fprint(output, strings.Repeat("x", maxOutputBytes+1))
		time.Sleep(30 * time.Second)
	case "unknown-field":
		_, _ = fmt.Fprint(os.Stdout, `{"unexpected":true}`)
	case "trailing":
		_, _ = fmt.Fprint(os.Stdout, `{} {}`)
	case "invalid-verdict":
		_, _ = fmt.Fprint(os.Stdout, `{"decision":"sometimes"}`)
	case "first", "second":
		data, _ := json.Marshal(hookcmd.Result{AdditionalContext: mode})
		_, _ = os.Stdout.Write(data)
	case "rewrite-input":
		data, _ := json.Marshal(hookcmd.Result{UpdatedInput: json.RawMessage(`{"step":"managed"}`)})
		_, _ = os.Stdout.Write(data)
	case "echo-input":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(3)
		}
		var event hookcmd.Event
		if json.Unmarshal(data, &event) != nil {
			os.Exit(3)
		}
		data, _ = json.Marshal(hookcmd.Result{AdditionalContext: string(event.ToolInput)})
		_, _ = os.Stdout.Write(data)
	case "rewrite-output":
		_, _ = fmt.Fprint(os.Stdout, `{"updated_output":{"step":"post"}}`)
	case "echo-output":
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			os.Exit(3)
		}
		var event hookcmd.Event
		if json.Unmarshal(data, &event) != nil {
			os.Exit(3)
		}
		data, _ = json.Marshal(hookcmd.Result{AdditionalContext: string(event.ToolResponse)})
		_, _ = os.Stdout.Write(data)
	case "array-output":
		_, _ = fmt.Fprint(os.Stdout, `{"updated_output":[]}`)
	case "count":
		file, err := os.OpenFile(os.Args[index+2], os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(3)
		}
		_, err = file.WriteString("1")
		_ = file.Close()
		if err != nil {
			os.Exit(3)
		}
	case "full-context":
		data, _ := json.Marshal(hookcmd.Result{AdditionalContext: strings.Repeat("x", maxContextBytes)})
		_, _ = os.Stdout.Write(data)
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func TestRunnerExecutesJSONInputAndStructuredContext(t *testing.T) {
	definition := helperDefinition(t, "context", "context")
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
	event := hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir(), Prompt: "literal $(touch should-not-exist)\nquoted \"request\""}
	result, err := runner.Run(t.Context(), event)
	if err != nil || result.AdditionalContext != event.Prompt {
		t.Fatalf("Run() = %+v, %v, want unchanged literal context", result, err)
	}
}

func TestRunnerHonorsSelectionAndNameOrder(t *testing.T) {
	first, second := helperDefinition(t, "a-first", "first"), helperDefinition(t, "b-second", "second")
	second.Provider, second.Matcher = "provider", "^chat$"
	disabled := helperDefinition(t, "disabled", "failure")
	disabled.Enabled = false
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{second, disabled, first}}})
	event := hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", Provider: "provider", Source: "chat", CWD: t.TempDir()}
	result, err := runner.Run(t.Context(), event)
	if err != nil || result.AdditionalContext != "first\nsecond" {
		t.Fatalf("matching hooks = %+v, %v", result, err)
	}
	event.Provider = "other"
	result, err = runner.Run(t.Context(), event)
	if err != nil || result.AdditionalContext != "first" {
		t.Fatalf("provider selection = %+v, %v", result, err)
	}
}

func TestRunnerFailsClosedOnCommandFailures(t *testing.T) {
	for _, mode := range []string{hookcmd.DecisionDeny, "exit-deny", "failure", "timeout", "oversize", "unknown-field", "trailing", "invalid-verdict"} {
		t.Run(mode, func(t *testing.T) {
			definition := helperDefinition(t, "guard", mode)
			if mode == "timeout" {
				definition.TimeoutSeconds = 1
			}
			runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
			_, err := runner.Run(t.Context(), hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
			want := hookcmd.ErrExecution
			if mode == hookcmd.DecisionDeny || mode == "exit-deny" {
				want = hookcmd.ErrDenied
			}
			if !errors.Is(err, want) {
				t.Fatalf("Run(%s) error = %v, want %v", mode, err, want)
			}
			if strings.Contains(err.Error(), "private-command-canary") {
				t.Fatal("process stderr leaked into public error")
			}
		})
	}
}

func TestRunnerPropagatesCancellationWithoutRunningCommand(t *testing.T) {
	definition := helperDefinition(t, "guard", "timeout")
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	started := time.Now()
	_, err := runner.Run(ctx, hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
	if !errors.Is(err, hookcmd.ErrExecution) || time.Since(started) > time.Second {
		t.Fatalf("canceled Run() error = %v, duration = %v", err, time.Since(started))
	}
}

func TestRunnerConfiguredTimeouts(t *testing.T) {
	for _, seconds := range []int{0, 3600} {
		t.Run(fmt.Sprint(seconds), func(t *testing.T) {
			definition := helperDefinition(t, "guard", "context")
			definition.TimeoutSeconds = seconds
			runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
			result, err := runner.Run(t.Context(), hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir(), Prompt: "configured timeout"})
			if err != nil || result.AdditionalContext != "configured timeout" {
				t.Fatalf("Run(timeout=%d) = %+v, %v", seconds, result, err)
			}
		})
	}
}

func TestRunnerNoDeadlineStillHonorsCallerCancellation(t *testing.T) {
	definition := helperDefinition(t, "guard", "timeout")
	definition.TimeoutSeconds = 0
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := runner.Run(ctx, hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
	if !errors.Is(err, hookcmd.ErrExecution) || time.Since(started) > 2*time.Second {
		t.Fatalf("canceled unlimited hook = %v, duration = %v", err, time.Since(started))
	}
}

func TestRunnerTimeoutIncludesWaitingForProcessSlot(t *testing.T) {
	definition := helperDefinition(t, "guard", "context")
	definition.TimeoutSeconds = 1
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
	for range cap(runner.gate) {
		runner.gate <- struct{}{}
	}
	started := time.Now()
	_, err := runner.Run(t.Context(), hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
	if !errors.Is(err, hookcmd.ErrExecution) || time.Since(started) > 3*time.Second {
		t.Fatalf("queued hook = %v, duration = %v", err, time.Since(started))
	}
}

func TestRunnerOutputLimitTerminatesUnlimitedCommand(t *testing.T) {
	for _, mode := range []string{"oversize-wait", "stderr-oversize-wait"} {
		t.Run(mode, func(t *testing.T) {
			definition := helperDefinition(t, "guard", mode)
			definition.TimeoutSeconds = 0
			runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			started := time.Now()
			_, err := runner.Run(ctx, hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
			if !errors.Is(err, hookcmd.ErrExecution) || ctx.Err() != nil {
				t.Fatalf("oversized unlimited command = %v, duration=%v, caller=%v", err, time.Since(started), ctx.Err())
			}
		})
	}
}

func TestRunnerAcceptsCompleteContextBudget(t *testing.T) {
	definition := helperDefinition(t, "context", "full-context")
	runner := NewManagedRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
	result, err := runner.Run(t.Context(), hookcmd.Event{Name: hookcmd.UserPromptSubmit, SessionID: "session", CWD: t.TempDir()})
	if err != nil || result.AdditionalContext != strings.Repeat("x", maxContextBytes) {
		t.Fatalf("full context budget = %d bytes, %v", len(result.AdditionalContext), err)
	}
}

func TestRunnerOrdersEffectiveDefinitionsAndChainsRewrites(t *testing.T) {
	managed := helperDefinition(t, "z-managed", "rewrite-input")
	managed.Event = hookcmd.PreToolUse
	packaged := helperDefinition(t, "a-package", "echo-input")
	packaged.Event = hookcmd.PreToolUse
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{
			{ID: "plugin/p/a-package", Definition: packaged, PluginID: "p", PluginRevision: "r1"},
			{ID: "managed/z-managed", Definition: managed},
		}}, nil
	}))
	result, err := runner.Run(t.Context(), hookcmd.Event{
		Name: hookcmd.PreToolUse, SessionID: "session", CWD: t.TempDir(),
		ToolName: "tool", ToolInput: json.RawMessage(`{"step":"original"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.UpdatedInput), `{"step":"managed"}`; got != want {
		t.Fatalf("UpdatedInput = %s, want %s", got, want)
	}
	if got, want := result.AdditionalContext, `{"step":"managed"}`; got != want {
		t.Fatalf("AdditionalContext = %q, want %q", got, want)
	}
}

func TestExecutionSnapshotRequiresCanonicalIdentityAndMetadata(t *testing.T) {
	definition := helperDefinition(t, "guard", "context")
	tests := []struct {
		name      string
		effective hookcmd.EffectiveDefinition
		wantValid bool
	}{
		{name: "managed", effective: hookcmd.EffectiveDefinition{ID: "managed/guard", Definition: definition}, wantValid: true},
		{name: "managed plugin metadata", effective: hookcmd.EffectiveDefinition{ID: "managed/guard", Definition: definition, PluginID: "plugin"}},
		{name: "managed mismatched name", effective: hookcmd.EffectiveDefinition{ID: "managed/other", Definition: definition}},
		{name: "plugin", effective: hookcmd.EffectiveDefinition{ID: "plugin/demo/guard", Definition: definition, PluginID: "demo", PluginRevision: "revision"}, wantValid: true},
		{name: "plugin missing id", effective: hookcmd.EffectiveDefinition{ID: "plugin//guard", Definition: definition, PluginRevision: "revision"}},
		{name: "plugin missing revision", effective: hookcmd.EffectiveDefinition{ID: "plugin/demo/guard", Definition: definition, PluginID: "demo"}},
		{name: "plugin mismatched metadata", effective: hookcmd.EffectiveDefinition{ID: "plugin/other/guard", Definition: definition, PluginID: "demo", PluginRevision: "revision"}},
		{name: "plugin id contains segment", effective: hookcmd.EffectiveDefinition{ID: "plugin/demo/nested/guard", Definition: definition, PluginID: "demo/nested", PluginRevision: "revision"}},
		{name: "arbitrary", effective: hookcmd.EffectiveDefinition{ID: "guard", Definition: definition}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := validExecutionSnapshot([]hookcmd.EffectiveDefinition{test.effective}); got != test.wantValid {
				t.Fatalf("validExecutionSnapshot() = %t, want %t", got, test.wantValid)
			}
		})
	}
}

func TestRunnerChainsAndValidatesUpdatedOutput(t *testing.T) {
	rewrite := helperDefinition(t, "rewrite", "rewrite-output")
	rewrite.Event = hookcmd.PostToolUse
	echo := helperDefinition(t, "verify", "echo-output")
	echo.Event = hookcmd.PostToolUse
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{
			{ID: "managed/rewrite", Definition: rewrite},
			{ID: "managed/verify", Definition: echo},
		}}, nil
	}))
	result, err := runner.Run(t.Context(), hookcmd.Event{
		Name: hookcmd.PostToolUse, SessionID: "session", CWD: t.TempDir(),
		ToolName: "tool", ToolInput: json.RawMessage(`{}`), ToolResponse: json.RawMessage(`{"step":"original"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(result.UpdatedOutput), `{"step":"post"}`; got != want {
		t.Fatalf("UpdatedOutput = %s, want %s", got, want)
	}
	if got, want := result.AdditionalContext, `{"step":"post"}`; got != want {
		t.Fatalf("AdditionalContext = %q, want %q", got, want)
	}

	invalid := helperDefinition(t, "invalid", "array-output")
	invalid.Event = hookcmd.PostToolUse
	invalidRunner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{{ID: "managed/invalid", Definition: invalid}}}, nil
	}))
	_, err = invalidRunner.Run(t.Context(), hookcmd.Event{Name: hookcmd.PostToolUse, SessionID: "session", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)})
	if !errors.Is(err, hookcmd.ErrExecution) {
		t.Fatalf("array UpdatedOutput error = %v, want ErrExecution", err)
	}
	invalid.Event = hookcmd.PreToolUse
	_, err = NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{{ID: "managed/invalid", Definition: invalid}}}, nil
	})).Run(t.Context(), hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)})
	if !errors.Is(err, hookcmd.ErrExecution) {
		t.Fatalf("PreToolUse UpdatedOutput error = %v, want ErrExecution", err)
	}
}

func TestRunnerCoalescesConcurrentCrossSourceCalls(t *testing.T) {
	path := filepath.Join(t.TempDir(), "executions")
	definition := helperDefinition(t, "count", "count")
	definition.Event = hookcmd.PreToolUse
	definition.Args = append(definition.Args, path)
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		return hookcmd.ExecutionSnapshot{Definitions: []hookcmd.EffectiveDefinition{{ID: "managed/count", Definition: definition}}}, nil
	}))
	event := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", ToolCallID: "call", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	const callers = 32
	start := make(chan struct{})
	errorsSeen := make(chan error, callers)
	var group sync.WaitGroup
	for i := range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			call := event
			if i%2 == 0 {
				call.Source = "adk"
			} else {
				call.Source = "native"
			}
			_, err := runner.Run(t.Context(), call)
			errorsSeen <- err
		}()
	}
	close(start)
	group.Wait()
	close(errorsSeen)
	for err := range errorsSeen {
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(data); got != 1 {
		t.Fatalf("hook executions = %d, want 1", got)
	}
}

func TestRunnerMissingIdentityBypassesCoalescing(t *testing.T) {
	var loads atomic.Int64
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		loads.Add(1)
		return hookcmd.ExecutionSnapshot{}, nil
	}))
	base := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", ToolCallID: "call", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	for _, missing := range []string{"invocation", "call"} {
		event := base
		switch missing {
		case "invocation":
			event.InvocationID = ""
		case "call":
			event.ToolCallID = ""
		}
		if _, err := runner.Run(t.Context(), event); err != nil {
			t.Fatalf("Run(missing %s): %v", missing, err)
		}
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("snapshot loads = %d, want 2", got)
	}
}

func TestRunnerDeduplicationCapacityFailsClosed(t *testing.T) {
	var loads atomic.Int64
	newRunner := func() *Runner {
		return NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
			loads.Add(1)
			return hookcmd.ExecutionSnapshot{}, nil
		}))
	}
	event := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	runner := newRunner()
	for i := range maxInvocationEntries {
		event.ToolCallID = fmt.Sprintf("call-%d", i)
		if _, err := runner.Run(t.Context(), event); err != nil {
			t.Fatalf("Run(%d): %v", i, err)
		}
	}
	event.ToolCallID = "over-capacity"
	if _, err := runner.Run(t.Context(), event); !errors.Is(err, hookcmd.ErrExecution) {
		t.Fatalf("per-invocation capacity error = %v, want ErrExecution", err)
	}
	if got := loads.Load(); got != maxInvocationEntries {
		t.Fatalf("snapshot loads = %d, want %d", got, maxInvocationEntries)
	}

	loads.Store(0)
	runner = newRunner()
	for i := range maxGlobalEntries {
		event.InvocationID = fmt.Sprintf("invocation-%d", i)
		event.ToolCallID = "call"
		if _, err := runner.Run(t.Context(), event); err != nil {
			t.Fatalf("global Run(%d): %v", i, err)
		}
	}
	event.InvocationID = "over-global-capacity"
	if _, err := runner.Run(t.Context(), event); !errors.Is(err, hookcmd.ErrExecution) {
		t.Fatalf("global capacity error = %v, want ErrExecution", err)
	}
	if got := loads.Load(); got != maxGlobalEntries {
		t.Fatalf("global snapshot loads = %d, want %d", got, maxGlobalEntries)
	}
}

func TestRunnerDeduplicationReleaseAndTTL(t *testing.T) {
	var loads atomic.Int64
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		loads.Add(1)
		return hookcmd.ExecutionSnapshot{}, nil
	}))
	now := time.Date(2026, time.October, 15, 12, 0, 0, 0, time.UTC)
	runner.dedup.now = func() time.Time { return now }
	event := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", ToolCallID: "call", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("cached loads = %d, want 1", got)
	}
	runner.ReleaseInvocation(event.SessionID, event.InvocationID)
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("loads after invocation release = %d, want 2", got)
	}
	runner.ReleaseSession(event.SessionID)
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 3 {
		t.Fatalf("loads after session release = %d, want 3", got)
	}
	now = now.Add(deduplicationTTL + time.Second)
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 4 {
		t.Fatalf("loads after TTL = %d, want 4", got)
	}
}

func TestRunnerCoalescesClassifiedFailure(t *testing.T) {
	var loads atomic.Int64
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		loads.Add(1)
		return hookcmd.ExecutionSnapshot{}, fmt.Errorf("private details: %w", hookcmd.ErrUnavailable)
	}))
	event := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", ToolCallID: "call", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	for range 2 {
		_, err := runner.Run(t.Context(), event)
		if !errors.Is(err, hookcmd.ErrUnavailable) || strings.Contains(err.Error(), "private details") {
			t.Fatalf("classified error = %v", err)
		}
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("snapshot loads = %d, want 1", got)
	}
}

func TestRunnerReleaseDuringExecutionRemovesCompletedDecision(t *testing.T) {
	var loads atomic.Int64
	started := make(chan struct{})
	unblock := make(chan struct{})
	runner := NewRunner(snapshotReaderFunc(func(context.Context) (hookcmd.ExecutionSnapshot, error) {
		if loads.Add(1) == 1 {
			close(started)
			<-unblock
		}
		return hookcmd.ExecutionSnapshot{}, nil
	}))
	event := hookcmd.Event{Name: hookcmd.PreToolUse, SessionID: "session", InvocationID: "invocation", ToolCallID: "call", CWD: t.TempDir(), ToolName: "tool", ToolInput: json.RawMessage(`{}`)}
	results := make(chan error, 1)
	go func() {
		_, err := runner.Run(t.Context(), event)
		results <- err
	}()
	<-started
	runner.ReleaseInvocation(event.SessionID, event.InvocationID)
	close(unblock)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 1 {
		t.Fatalf("in-flight snapshot loads = %d, want 1", got)
	}
	if _, err := runner.Run(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	if got := loads.Load(); got != 2 {
		t.Fatalf("snapshot loads after released execution = %d, want 2", got)
	}
}
