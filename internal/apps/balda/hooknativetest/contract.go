package hooknativetest

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

// MessageLimit is the maximum normalized native hook request or response size.
const MessageLimit = 256 << 10

// Runner executes one normalized hook event for a contract fixture.
type Runner func(context.Context, hookcmd.Event) (hookcmd.Result, error)

// Binding exercises one authenticated, provider-neutral Event/Result binding.
type Binding interface {
	Invoke(ctx context.Context, event hookcmd.Event) (hookcmd.Result, error)
	InvokeUnauthenticated(ctx context.Context, event hookcmd.Event) error
	InvokeWithOrigin(ctx context.Context, event hookcmd.Event) error
	SignalReady(ctx context.Context) error
	WaitReady(ctx context.Context) error
	Release() error
}

// BindingFixture supplies a binding and the trusted context it must enforce.
type BindingFixture struct {
	Binding Binding
	Trusted hookcmd.Event
}

// BindingFactory creates an isolated binding backed by runner.
type BindingFactory func(testing.TB, Runner) BindingFixture

// RunBindingContract verifies the provider-neutral authenticated binding.
func RunBindingContract(t *testing.T, factory BindingFactory) {
	t.Helper()
	t.Run("normalized event and result", func(t *testing.T) {
		var observed hookcmd.Event
		want := hookcmd.Result{Decision: hookcmd.DecisionAllow, AdditionalContext: "checked", UpdatedInput: json.RawMessage(`{"command":"approved"}`)}
		fixture := factory(t, func(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
			observed = event
			return want, nil
		})
		got, err := fixture.Binding.Invoke(t.Context(), hookcmd.Event{
			Name:       hookcmd.PreToolUse,
			SessionID:  "untrusted-session",
			Provider:   "untrusted-provider",
			CWD:        "/untrusted",
			ToolName:   "Bash",
			ToolCallID: "call-1",
			ToolInput:  json.RawMessage(`{"command":"original"}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if observed.SessionID != fixture.Trusted.SessionID || observed.Provider != fixture.Trusted.Provider || observed.CWD != fixture.Trusted.CWD || observed.Source != fixture.Trusted.Source {
			t.Fatalf("trusted context = %+v, want %+v", observed, fixture.Trusted)
		}
		if observed.Name != hookcmd.PreToolUse || observed.ToolName != "Bash" || observed.ToolCallID != "call-1" || string(observed.ToolInput) != `{"command":"original"}` {
			t.Fatalf("normalized tool event = %+v", observed)
		}
		if got.Decision != want.Decision || got.AdditionalContext != want.AdditionalContext || string(got.UpdatedInput) != string(want.UpdatedInput) {
			t.Fatalf("normalized result = %+v, want %+v", got, want)
		}
	})

	t.Run("authentication", func(t *testing.T) {
		fixture := factory(t, allow)
		event := hookcmd.Event{Name: hookcmd.PreToolUse}
		if err := fixture.Binding.InvokeUnauthenticated(t.Context(), event); !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("unauthenticated invocation = %v", err)
		}
		if err := fixture.Binding.InvokeWithOrigin(t.Context(), event); !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("cross-origin invocation = %v", err)
		}
	})

	t.Run("bounds", func(t *testing.T) {
		requestFixture := factory(t, allow)
		oversized := hookcmd.Event{Name: hookcmd.PreToolUse, ToolInput: json.RawMessage(`{"value":"` + strings.Repeat("x", MessageLimit) + `"}`)}
		if _, err := requestFixture.Binding.Invoke(t.Context(), oversized); !errors.Is(err, hookcmd.ErrInvalid) {
			t.Fatalf("oversized request = %v", err)
		}
		responseFixture := factory(t, func(context.Context, hookcmd.Event) (hookcmd.Result, error) {
			return hookcmd.Result{AdditionalContext: strings.Repeat("x", MessageLimit)}, nil
		})
		if _, err := responseFixture.Binding.Invoke(t.Context(), hookcmd.Event{Name: hookcmd.PreToolUse}); !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("oversized response = %v", err)
		}
	})

	t.Run("readiness and revocation", func(t *testing.T) {
		fixture := factory(t, allow)
		finished := make(chan error, 1)
		go func() { finished <- fixture.Binding.WaitReady(t.Context()) }()
		select {
		case err := <-finished:
			t.Fatalf("readiness wait completed before signal: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		if err := fixture.Binding.SignalReady(t.Context()); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("readiness wait did not observe signal")
		}
		if err := fixture.Binding.Release(); err != nil {
			t.Fatal(err)
		}
		if err := fixture.Binding.SignalReady(t.Context()); !errors.Is(err, hookcmd.ErrUnavailable) {
			t.Fatalf("released readiness capability = %v", err)
		}
	})

	t.Run("release cancels invocation", func(t *testing.T) {
		started := make(chan struct{})
		fixture := factory(t, func(ctx context.Context, _ hookcmd.Event) (hookcmd.Result, error) {
			close(started)
			<-ctx.Done()
			return hookcmd.Result{}, ctx.Err()
		})
		finished := make(chan error, 1)
		go func() {
			_, err := fixture.Binding.Invoke(t.Context(), hookcmd.Event{Name: hookcmd.PreToolUse})
			finished <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("hook invocation did not start")
		}
		if err := fixture.Binding.Release(); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-finished:
			if !errors.Is(err, hookcmd.ErrUnavailable) {
				t.Fatalf("released invocation = %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("hook invocation survived binding release")
		}
	})
}

func allow(context.Context, hookcmd.Event) (hookcmd.Result, error) {
	return hookcmd.Result{}, nil
}

// LaunchFixture is a provider-neutral native adapter launch request.
type LaunchFixture struct {
	Command []string
	Model   string
	Mode    string
}

// PreparedLaunch exposes observable launch properties without prescribing an
// adapter's command, environment, or wire representation.
type PreparedLaunch struct {
	Command          []string
	Model            string
	Mode             string
	BindingReference string
}

// ToolCall is one observable native tool execution.
type ToolCall struct {
	Name     string
	CallID   string
	Input    json.RawMessage
	Response json.RawMessage
}

// ToolObservation describes the effect visible after a native adapter runs.
type ToolObservation struct {
	Executed       bool
	EffectiveInput json.RawMessage
	PostContext    string
	Feedback       string
}

// Adapter exercises one concrete provider's launch and native wire protocol.
type Adapter interface {
	Launch() PreparedLaunch
	RunTool(ctx context.Context, call ToolCall) (ToolObservation, error)
	Close() error
}

// AdapterFactory creates a real protocol fixture backed by normalized hooks.
type AdapterFactory func(testing.TB, LaunchFixture, Runner) Adapter

// RunAdapterContract verifies behavior shared by concrete native adapters.
// Claude, Codex, and OpenCode adapter packages call this contract with their
// actual launch mutation and wire-protocol fixtures after extraction.
func RunAdapterContract(t *testing.T, factory AdapterFactory) {
	t.Helper()
	launch := LaunchFixture{Command: []string{"fixture-agent", "serve"}, Model: "fixture-model", Mode: "fixture-mode"}
	t.Run("launch", func(t *testing.T) {
		wantCommand := slices.Clone(launch.Command)
		adapter := factory(t, launch, allow)
		t.Cleanup(func() { _ = adapter.Close() })
		prepared := adapter.Launch()
		if !slices.Equal(launch.Command, wantCommand) {
			t.Fatalf("adapter mutated caller-owned command: got %q, want %q", launch.Command, wantCommand)
		}
		if prepared.Model != launch.Model || prepared.Mode != launch.Mode || prepared.BindingReference == "" || !containsSequence(prepared.Command, wantCommand) {
			t.Fatalf("prepared launch = %+v, want preserved command/settings and injected binding", prepared)
		}
	})
	for _, test := range []struct {
		name   string
		runner Runner
		check  func(*testing.T, ToolObservation)
	}{
		{name: "allow", runner: allow, check: func(t *testing.T, got ToolObservation) {
			if !got.Executed || string(got.EffectiveInput) != `{"command":"original"}` {
				t.Fatalf("allow observation = %+v", got)
			}
		}},
		{name: "deny", runner: func(context.Context, hookcmd.Event) (hookcmd.Result, error) {
			return hookcmd.Result{Decision: hookcmd.DecisionDeny, Reason: "blocked"}, nil
		}, check: func(t *testing.T, got ToolObservation) {
			if got.Executed || !strings.Contains(got.Feedback, "blocked") {
				t.Fatalf("deny observation = %+v", got)
			}
		}},
		{name: "rewrite", runner: func(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name == hookcmd.PreToolUse {
				return hookcmd.Result{UpdatedInput: json.RawMessage(`{"command":"approved"}`)}, nil
			}
			return hookcmd.Result{}, nil
		}, check: func(t *testing.T, got ToolObservation) {
			if !got.Executed || string(got.EffectiveInput) != `{"command":"approved"}` {
				t.Fatalf("rewrite observation = %+v", got)
			}
		}},
		{name: "post", runner: func(_ context.Context, event hookcmd.Event) (hookcmd.Result, error) {
			if event.Name == hookcmd.PostToolUse {
				return hookcmd.Result{AdditionalContext: "checked result"}, nil
			}
			return hookcmd.Result{}, nil
		}, check: func(t *testing.T, got ToolObservation) {
			if !got.Executed || !strings.Contains(got.PostContext, "checked result") {
				t.Fatalf("post observation = %+v", got)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			adapter := factory(t, launch, test.runner)
			t.Cleanup(func() { _ = adapter.Close() })
			got, err := adapter.RunTool(t.Context(), ToolCall{Name: "shell", CallID: "call-1", Input: json.RawMessage(`{"command":"original"}`), Response: json.RawMessage(`{"output":"done"}`)})
			if err != nil {
				t.Fatal(err)
			}
			test.check(t, got)
		})
	}
}

func containsSequence(values, sequence []string) bool {
	for len(values) >= len(sequence) {
		if slices.Equal(values[:len(sequence)], sequence) {
			return true
		}
		values = values[1:]
	}
	return len(sequence) == 0
}
