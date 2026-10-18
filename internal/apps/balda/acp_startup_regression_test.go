package balda

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/catalogapp"
	"github.com/baldaworks/balda/internal/apps/balda/channel/telegram"
	"github.com/baldaworks/balda/internal/apps/balda/internalmcp"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	runtimeconfig "github.com/baldaworks/runtime/v2/appconfig"
	"go.uber.org/fx"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

const (
	startupUserAuthor     = "user"
	startupParentDeadline = "parent-deadline"
	startupInitialize     = "initialize"
	startupResume         = "session/resume"
)

// These fixtures use the production Module and lifecycle owners, without starting
// credential-dependent channel ingress. Every ACP request crosses native stdio.
type startupComposition struct {
	sessions *baldasession.Manager
	runtime  *baldaagent.RuntimeManager
	provider state.Provider
	bundled  *internalmcp.InternalMCPManager
	catalog  *catalogapp.Lifecycle
}

func newStartupComposition(t *testing.T, cfg Config, norma runtimeconfig.RuntimeConfig) *startupComposition {
	t.Helper()
	f := new(startupComposition)
	app := fx.New(Module(cfg, norma, "", runtimeconfig.RuntimeLoadOptions{}, nil), fx.NopLogger,
		fx.Populate(&f.sessions, &f.runtime, &f.provider, &f.bundled, &f.catalog))
	if err := app.Err(); err != nil {
		t.Fatal(err)
	}
	for _, start := range []func(context.Context) error{f.bundled.EnsureStarted, f.catalog.Start, f.runtime.EnsureRuntime, f.sessions.Start} {
		if err := start(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() { f.close(t) })
	return f
}

func (f *startupComposition) close(t *testing.T) {
	t.Helper()
	if f.provider == nil {
		return
	}
	for _, stop := range []func(context.Context) error{f.sessions.Stop, f.runtime.Stop, f.catalog.Stop, f.bundled.Stop} {
		if err := stop(context.Background()); err != nil {
			t.Errorf("stop startup fixture: %v", err)
		}
	}
	if err := f.provider.Close(); err != nil {
		t.Errorf("close startup fixture: %v", err)
	}
	f.provider = nil
}

func startupRegressionConfig(t *testing.T, budget, mode string) (Config, runtimeconfig.RuntimeConfig, string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("CODEX_HOME", home)
	cfg, norma, trace := acpWiringConfig(t, budget, "0s")
	cfg.Balda.Sessions.Persistence = "sqlite"
	provider := norma.Providers["fixture"]
	provider.GenericACP.Cmd = []string{provider.GenericACP.Cmd[0], "-test.run=^TestBaldaACPRegressionChild$", "--", mode, trace, "literal startup argument"}
	norma.Providers["fixture"] = provider
	return cfg, norma, trace
}

func TestACPColdRestoredFirstTurn(t *testing.T) {
	for _, tt := range []struct{ name, budget, delay string }{
		{"explicit", "60s", "11s"}, {"omitted-default", "", "10.88s"}, {"healthy", "60s", "20ms"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, norma, trace := startupRegressionConfig(t, tt.budget, tt.delay)
			first := newStartupComposition(t, cfg, norma)
			locator := telegram.NewLocator(101, 77)
			original, err := first.sessions.EnsureSession(t.Context(), baldasession.SessionContext{Locator: locator, UserID: "saved-user"}, "saved-label")
			if err != nil {
				t.Fatal(err)
			}
			record, found, err := first.provider.Sessions().GetByAddress(t.Context(), locator.ChannelType, locator.AddressKey)
			if err != nil || !found {
				t.Fatalf("saved metadata: %v, %v", found, err)
			}
			saved, err := first.provider.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: "norma-balda", UserID: original.GetUserID(), SessionID: original.GetAgentSessionID()})
			if err != nil {
				t.Fatal(err)
			}
			event := adksession.NewEvent(t.Context(), "persisted-turn")
			event.Author = startupUserAuthor
			event.Content = genai.NewContentFromText("exact saved history", genai.RoleUser)
			event.Actions.StateDelta = map[string]any{"saved_value": "keep", "acp_session": map[string]any{"session_id": "fixture-session"}}
			if err := first.provider.RuntimeSessions().AppendEvent(t.Context(), saved.Session, event); err != nil {
				t.Fatal(err)
			}
			first.close(t)
			second := newStartupComposition(t, cfg, norma)
			inbound := locator
			inbound.SessionID = "different-inbound-id"
			restored, err := second.sessions.RestoreSession(t.Context(), baldasession.SessionContext{Locator: inbound, UserID: "requester"})
			if err != nil {
				t.Fatal(err)
			}
			if restored.GetSessionID() != record.SessionID || restored.GetUserID() != record.UserID || restored.GetRuntimeSnapshotID() != record.RuntimeSnapshotID || restored.GetArchiveID() != record.ArchiveID {
				t.Fatalf("exact restore changed identity or pin: %+v", record)
			}
			assertStartupHistory(t, second.provider, restored, event)
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatalf("startup/metadata restore launched ACP: %v", err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			if err := startupSessionTurn(ctx, restored, "first restored message"); err != nil {
				t.Fatalf("first provider attempt: %v", err)
			}
			if err := startupSessionTurn(ctx, restored, "warm message"); err != nil {
				t.Fatalf("warm provider attempt: %v", err)
			}
			assertStartupHistory(t, second.provider, restored, event)
			after, _, err := second.provider.Sessions().GetByAddress(t.Context(), locator.ChannelType, locator.AddressKey)
			if err != nil || !reflect.DeepEqual(after, record) {
				t.Fatalf("turn replaced saved metadata: %+v, %v", after, err)
			}
			records := readStartupTrace(t, trace)
			counts := make(map[string]int)
			for _, r := range records {
				counts[r.Method]++
			}
			if counts["launch"] != 1 || counts[startupInitialize] != 1 || counts["session/new"] != 0 || counts[startupResume] != 0 || counts["session/prompt"] != 2 {
				t.Fatalf("cold/warm method counts = %v", counts)
			}
			wantArgs := norma.Providers["fixture"].GenericACP.Cmd[1:]
			if !reflect.DeepEqual(records[0].Args, wantArgs) {
				t.Fatalf("backend argv changed: %q, want %q", records[0].Args, wantArgs)
			}
			pid := records[0].PID
			second.close(t)
			assertStartupReaped(t, pid, time.Now())
		})
	}
}

func assertStartupHistory(t *testing.T, provider state.Provider, sess *baldasession.TopicSession, expected *adksession.Event) {
	t.Helper()
	current, err := provider.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: "norma-balda", UserID: sess.GetUserID(), SessionID: sess.GetAgentSessionID()})
	if err != nil {
		t.Fatal(err)
	}
	if current.Session.Events().Len() == 0 {
		t.Fatal("saved history was reset")
	}
	for event := range current.Session.Events().All() {
		if event.ID != expected.ID {
			continue
		}
		if !reflect.DeepEqual(event.Content, expected.Content) {
			t.Fatalf("saved history changed: %+v", event.Content)
		}
		value, err := current.Session.State().Get("saved_value")
		if err != nil || value != "keep" {
			t.Fatalf("saved state changed: %v, %v", value, err)
		}
		return
	}
	t.Fatal("saved event disappeared")
}

func startupSessionTurn(ctx context.Context, sess *baldasession.TopicSession, text string) error {
	var response strings.Builder
	for event, err := range sess.GetRunner().Run(ctx, sess.GetUserID(), sess.GetAgentSessionID(), genai.NewContentFromText(text, genai.RoleUser), adkagent.RunConfig{}) {
		if err != nil {
			return err
		}
		if event != nil && event.Content != nil {
			for _, part := range event.Content.Parts {
				response.WriteString(part.Text)
			}
		}
	}
	if !strings.Contains(response.String(), "native ACP response") {
		return fmt.Errorf("missing native response: %q", response.String())
	}
	return nil
}

type startupTrace struct {
	Method string   `json:"method"`
	PID    int      `json:"pid,omitempty"`
	Args   []string `json:"args,omitempty"`
}

func readStartupTrace(t *testing.T, path string) []startupTrace {
	t.Helper()
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	var records []startupTrace
	decoder := json.NewDecoder(file)
	for {
		var r startupTrace
		if err := decoder.Decode(&r); errors.Is(err, io.EOF) {
			return records
		} else if err != nil {
			t.Fatal(err)
		}
		records = append(records, r)
	}
}

func awaitStartupMethod(t *testing.T, path, method string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		records := readStartupTrace(t, path)
		for _, r := range records {
			if r.Method == method {
				return records[0].PID
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("ACP never reached %s", method)
	return 0
}

func assertStartupReaped(t *testing.T, pid int, signal time.Time) {
	t.Helper()
	deadline := signal.Add(6200 * time.Millisecond)
	for {
		alive, err := startupProcessAlive(pid)
		if err != nil {
			t.Fatal(err)
		}
		if !alive {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("owned child %d still exists after cleanup bound", pid)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestACPStartupLifecycle(t *testing.T) {
	for _, tt := range []struct{ name, budget string }{
		{"timeout-short", "400ms"}, {"timeout-long", "800ms"}, {"cancel", "60s"}, {startupParentDeadline, "60s"}, {"shutdown", "60s"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg, norma, trace := startupRegressionConfig(t, tt.budget, "hang-initialize")
			f := newStartupComposition(t, cfg, norma)
			sess, err := f.sessions.EnsureSession(t.Context(), baldasession.SessionContext{Locator: telegram.NewLocator(101, 0), UserID: "fixture-user"}, "fixture")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			if tt.name == startupParentDeadline {
				ctx, cancel = context.WithTimeout(t.Context(), time.Second)
			}
			defer cancel()
			done := make(chan error, 1)
			started := time.Now()
			go func() { done <- startupSessionTurn(ctx, sess, "hello") }()
			pid := awaitStartupMethod(t, trace, startupInitialize)
			signal := time.Now()
			want := context.DeadlineExceeded
			switch tt.name {
			case "cancel":
				cancel()
				want = context.Canceled
			case "shutdown":
				stopped := make(chan error, 1)
				go func() {
					stopped <- errors.Join(f.sessions.Stop(context.Background()), f.runtime.Stop(context.Background()))
				}()
				select {
				case err := <-stopped:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(time.Until(signal.Add(6200 * time.Millisecond))):
					t.Fatal("shutdown waited beyond independent cleanup bound")
				}
				want = context.Canceled
			default:
				budget, _ := time.ParseDuration(tt.budget)
				if tt.name == startupParentDeadline {
					budget = time.Second
				}
				signal = started.Add(budget)
			}
			select {
			case err := <-done:
				if !errors.Is(err, want) {
					t.Fatalf("startup cause = %v; want %v", err, want)
				}
			case <-time.After(time.Until(signal.Add(6200 * time.Millisecond))):
				t.Fatal("startup did not return within cleanup bound")
			}
			if tt.name == "timeout-short" || tt.name == "timeout-long" || tt.name == startupParentDeadline {
				if now := time.Now(); now.Before(signal.Add(-50*time.Millisecond)) || now.After(signal.Add(1200*time.Millisecond)) {
					t.Fatalf("startup ignored selected budget: return offset %s", now.Sub(signal))
				}
			}
			assertStartupReaped(t, pid, signal)
			counts := make(map[string]int)
			for _, r := range readStartupTrace(t, trace) {
				counts[r.Method]++
			}
			if counts["launch"] != 1 || counts[startupInitialize] != 1 || counts["session/new"] != 0 {
				t.Fatalf("failed startup retried or attached: %v", counts)
			}
		})
	}
}

func TestACPAttachmentKeepsShortBudget(t *testing.T) {
	for _, method := range []string{"session/new", startupResume} {
		for _, caller := range []time.Duration{30 * time.Second, time.Second} {
			t.Run(method+"/"+caller.String(), func(t *testing.T) {
				cfg, norma, trace := startupRegressionConfig(t, "60s", "hang-"+method)
				f := newStartupComposition(t, cfg, norma)
				sess, err := f.sessions.EnsureSession(t.Context(), baldasession.SessionContext{Locator: telegram.NewLocator(101, 0), UserID: "fixture-user"}, "fixture")
				if err != nil {
					t.Fatal(err)
				}
				if method == startupResume {
					runtime, err := f.provider.RuntimeSessions().Get(t.Context(), &adksession.GetRequest{AppName: "norma-balda", UserID: sess.GetUserID(), SessionID: sess.GetAgentSessionID()})
					if err != nil {
						t.Fatal(err)
					}
					event := adksession.NewEvent(t.Context(), "remote-session")
					event.Author = startupUserAuthor
					event.Actions.StateDelta = map[string]any{"acp_session": map[string]any{"session_id": "fixture-session"}}
					if err := f.provider.RuntimeSessions().AppendEvent(t.Context(), runtime.Session, event); err != nil {
						t.Fatal(err)
					}
				}
				ctx, cancel := context.WithTimeout(t.Context(), caller)
				defer cancel()
				done := make(chan error, 1)
				started := time.Now()
				go func() { done <- startupSessionTurn(ctx, sess, "hello") }()
				pid := awaitStartupMethod(t, trace, method)
				budget := min(caller, 10*time.Second)
				select {
				case err := <-done:
					if !errors.Is(err, context.DeadlineExceeded) {
						t.Fatalf("attachment cause = %v", err)
					}
				case <-time.After(time.Until(started.Add(budget + time.Second))):
					t.Fatal("attachment exceeded its short deadline plus scheduling tolerance")
				}
				if elapsed := time.Since(started); elapsed < budget-50*time.Millisecond || elapsed > budget+time.Second {
					t.Fatalf("attachment elapsed %s; budget %s", elapsed, budget)
				}
				// Attachment timeout preserves the initialized process; the owner
				// closes it during shutdown, independently of the startup budget.
				shutdown := time.Now()
				f.close(t)
				assertStartupReaped(t, pid, shutdown)
				counts := make(map[string]int)
				for _, r := range readStartupTrace(t, trace) {
					counts[r.Method]++
				}
				wantPrompts := 0
				if method == startupResume {
					wantPrompts = 1
				}
				if counts[method] != 1 || counts["session/prompt"] != wantPrompts {
					t.Fatalf("attachment retried or prompted: %v", counts)
				}
			})
		}
	}
}

func TestBaldaACPRegressionChild(t *testing.T) {
	if len(os.Args) < 5 || os.Args[len(os.Args)-4] != "--" {
		t.Skip("native subprocess fixture")
	}
	mode, path := os.Args[len(os.Args)-3], os.Args[len(os.Args)-2]
	trace, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		os.Exit(2)
	}
	log := json.NewEncoder(trace)
	if err := log.Encode(startupTrace{Method: "launch", PID: os.Getpid(), Args: os.Args[1:]}); err != nil {
		os.Exit(2)
	}
	decoder, encoder := json.NewDecoder(os.Stdin), json.NewEncoder(os.Stdout)
	for {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := decoder.Decode(&req); err != nil {
			os.Exit(0)
		}
		if err := log.Encode(startupTrace{Method: req.Method}); err != nil {
			os.Exit(2)
		}
		var result any
		switch req.Method {
		case startupInitialize:
			if mode == "hang-initialize" {
				for {
					time.Sleep(time.Hour)
				}
			}
			delay := time.Duration(0)
			if !strings.HasPrefix(mode, "hang-") {
				var err error
				delay, err = time.ParseDuration(mode)
				if err != nil {
					os.Exit(2)
				}
			}
			time.Sleep(delay)
			result = map[string]any{"protocolVersion": 1, "agentCapabilities": map[string]any{"sessionCapabilities": map[string]any{"resume": map[string]any{}}}}
		case "session/new", startupResume:
			if mode == "hang-"+req.Method {
				for {
					time.Sleep(time.Hour)
				}
			}
			result = map[string]any{"sessionId": "fixture-session"}
		case "session/prompt":
			if mode == "hang-session/resume" {
				if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{"code": -32000, "message": "session not found"}}); err != nil {
					os.Exit(2)
				}
				continue
			}
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "method": "session/update", "params": map[string]any{"sessionId": "fixture-session", "update": map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]any{"type": "text", "text": "native ACP response"}}}}); err != nil {
				os.Exit(2)
			}
			result = map[string]any{"stopReason": "end_turn"}
		default:
			continue
		}
		if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result}); err != nil {
			os.Exit(2)
		}
	}
}
