package hooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

type recordFixture struct {
	record hookcmd.Record
	err    error
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
	default:
		os.Exit(3)
	}
	os.Exit(0)
}

func TestRunnerExecutesJSONInputAndStructuredContext(t *testing.T) {
	definition := helperDefinition(t, "context", "context")
	runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
	runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{second, disabled, first}}})
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
			runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
	runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
			runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
	runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
	runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
			runner := NewRunner(recordFixture{record: hookcmd.Record{Version: 1, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano), Definitions: []hookcmd.Definition{definition}}})
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
