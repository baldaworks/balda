package mcpfx

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	diagnosticInitializeMethod = "initialize"
	diagnosticListToolsMethod  = "tools/list"
)

type diagnosticValues struct {
	values mcpcmd.LaunchValues
	err    error
}

func (v diagnosticValues) ResolveValues(mcpcmd.Revision) (mcpcmd.LaunchValues, error) {
	return v.values, v.err
}

type diagnosticTokens struct {
	token string
	err   error
}

func (t diagnosticTokens) DiagnosticAccessToken(context.Context, mcpcmd.AuthBinding, []string) (string, error) {
	return t.token, t.err
}

func TestDiagnosticProbeCompletesMCPPaginationWithResolvedCredentials(t *testing.T) {
	const (
		staticSecret = "static-diagnostic-fixture"
		accessToken  = "oauth-diagnostic-fixture"
	)
	var (
		mu      sync.Mutex
		methods []string
	)
	peer := mcp.NewServer(&mcp.Implementation{Name: "diagnostic", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"first", "second", "third"} {
		mcp.AddTool(peer, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return peer }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Diagnostic") != staticSecret || r.Header.Get("Authorization") != "Bearer "+accessToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Method string `json:"method"`
			}
			if err := json.Unmarshal(body, &request); err == nil && (request.Method == diagnosticInitializeMethod || request.Method == diagnosticListToolsMethod) {
				mu.Lock()
				methods = append(methods, request.Method)
				mu.Unlock()
			}
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	binding := &mcpcmd.AuthBinding{ConnectionID: "worker", Resource: upstream.URL, Issuer: "https://issuer.example", ClientID: "client"}
	probe, err := NewDiagnosticProbe(
		diagnosticValues{values: mcpcmd.LaunchValues{Headers: map[string]string{"X-Diagnostic": staticSecret}}},
		diagnosticTokens{token: accessToken},
		NewClientLauncher(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{
		ConnectionID: "worker",
		ID:           "exact-revision",
		Definition: mcpcmd.Definition{
			Transport:   mcpcmd.TransportHTTP,
			URL:         upstream.URL,
			OAuth:       true,
			AuthBinding: binding,
			Scopes:      []string{"tools"},
		},
	})
	if result.Outcome != mcpcmd.DiagnosticSucceeded || result.ToolCount != 3 {
		t.Fatalf("DiagnoseMCP() = %+v, want succeeded with 3 tools", result)
	}
	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	mu.Unlock()
	if len(gotMethods) != 4 || gotMethods[0] != diagnosticInitializeMethod || !slices.Equal(gotMethods[1:], []string{diagnosticListToolsMethod, diagnosticListToolsMethod, diagnosticListToolsMethod}) {
		t.Fatalf("MCP methods = %v, want initialize then every tools/list page", gotMethods)
	}
}

func TestDiagnosticProbeNeverSendsCredentialsAcrossOrigins(t *testing.T) {
	const secret = "cross-origin-diagnostic-fixture"
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Diagnostic") != "" || r.Header.Get("Authorization") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	probe, err := NewDiagnosticProbe(
		diagnosticValues{values: mcpcmd.LaunchValues{Headers: map[string]string{"X-Diagnostic": secret}}},
		diagnosticTokens{token: secret},
		NewClientLauncher(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{
		ConnectionID: "worker",
		ID:           "exact-revision",
		Definition: mcpcmd.Definition{
			Transport: mcpcmd.TransportHTTP,
			URL:       origin.URL,
			OAuth:     true,
			AuthBinding: &mcpcmd.AuthBinding{
				ConnectionID: "worker", Resource: origin.URL, Issuer: "https://issuer.example", ClientID: "client",
			},
		},
	})
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 || leaked.Load() {
		t.Fatalf("cross-origin diagnostic = %+v, leaked=%v", result, leaked.Load())
	}
}

func TestDiagnosticProbeReturnsSecretFreeFailure(t *testing.T) {
	const secret = "diagnostic-error-secret-fixture"
	for _, tc := range []struct {
		name    string
		handler http.Handler
	}{
		{
			name: "authentication rejection",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				http.Error(w, secret, http.StatusUnauthorized)
			}),
		},
		{
			name: "malformed protocol response",
			handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(secret))
			}),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(tc.handler)
			defer upstream.Close()
			probe, err := NewDiagnosticProbe(
				diagnosticValues{values: mcpcmd.LaunchValues{Headers: map[string]string{"X-Diagnostic": secret}}},
				diagnosticTokens{},
				NewClientLauncher(),
			)
			if err != nil {
				t.Fatal(err)
			}
			result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{
				ConnectionID: "worker",
				ID:           "exact-revision",
				Definition:   mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL},
			})
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 || bytes.Contains(encoded, []byte(secret)) {
				t.Fatalf("failed diagnostic exposed private detail: %s", encoded)
			}
		})
	}
}

func TestDiagnosticProbeHonorsOperationAndCallerCancellation(t *testing.T) {
	release := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer func() {
		close(release)
		upstream.Close()
	}()
	for _, tc := range []struct {
		name   string
		caller func(context.Context) (context.Context, context.CancelFunc)
	}{
		{
			name: "operation deadline",
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				return ctx, func() {}
			},
		},
		{
			name: "caller cancellation",
			caller: func(ctx context.Context) (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(ctx)
				time.AfterFunc(20*time.Millisecond, cancel)
				return ctx, cancel
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe, err := NewDiagnosticProbe(diagnosticValues{}, diagnosticTokens{}, NewClientLauncher())
			if err != nil {
				t.Fatal(err)
			}
			probe.timeout = 40 * time.Millisecond
			ctx, cancel := tc.caller(t.Context())
			defer cancel()
			started := time.Now()
			result := probe.DiagnoseMCP(ctx, mcpcmd.Revision{ConnectionID: "worker", ID: "exact-revision", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL}})
			if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
				t.Fatalf("canceled diagnostic = %+v", result)
			}
			if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
				t.Fatalf("canceled diagnostic returned after %v", elapsed)
			}
		})
	}
}

func TestDiagnosticProbeWaitsForCanceledDiscoverySessionCleanup(t *testing.T) {
	for iteration := range 12 {
		t.Run(fmt.Sprintf("iteration-%02d", iteration), testDiagnosticProbeWaitsForCanceledDiscoverySessionCleanup)
	}
}

func testDiagnosticProbeWaitsForCanceledDiscoverySessionCleanup(t *testing.T) {
	peer := mcp.NewServer(&mcp.Implementation{Name: "canceled-discovery", Version: "1"}, &mcp.ServerOptions{PageSize: 1})
	for _, name := range []string{"first", "second"} {
		mcp.AddTool(peer, &mcp.Tool{Name: name}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
			return &mcp.CallToolResult{}, nil, nil
		})
	}
	normal := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return peer }, nil)
	laterPage := make(chan struct{})
	pageDrained := make(chan struct{})
	deleteDrained := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodDelete {
			time.Sleep(10 * time.Millisecond)
			w.WriteHeader(http.StatusAccepted)
			close(deleteDrained)
			return
		}
		if r.Method == http.MethodPost {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
			var request struct {
				Method string `json:"method"`
				Params struct {
					Cursor string `json:"cursor"`
				} `json:"params"`
			}
			if err := json.Unmarshal(body, &request); err == nil && request.Method == diagnosticListToolsMethod && request.Params.Cursor != "" {
				close(laterPage)
				<-r.Context().Done()
				close(pageDrained)
				return
			}
		}
		normal.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	probe, err := NewDiagnosticProbe(diagnosticValues{}, diagnosticTokens{}, NewClientLauncher())
	if err != nil {
		t.Fatal(err)
	}
	probe.timeout = 60 * time.Millisecond
	probe.closeTimeout = 250 * time.Millisecond
	result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{
		ConnectionID: "worker",
		ID:           "exact-revision",
		Definition:   mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL},
	})
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
		t.Fatalf("canceled discovery diagnostic = %+v", result)
	}
	for name, signal := range map[string]<-chan struct{}{
		"later tools page": laterPage,
		"tools request":    pageDrained,
		"session delete":   deleteDrained,
	} {
		select {
		case <-signal:
		default:
			t.Errorf("diagnostic returned before %s terminated", name)
		}
	}
}

type diagnosticInstance struct {
	closed chan struct{}
	tools  []mcpruntime.Tool
}

func (i diagnosticInstance) Tools() []mcpruntime.Tool { return i.tools }

func (i diagnosticInstance) Close(ctx context.Context) error {
	close(i.closed)
	<-ctx.Done()
	return errors.New("private close failure")
}

type diagnosticLauncherFixture struct{ instance mcpruntime.Instance }

func (l diagnosticLauncherFixture) StartDiagnostic(context.Context, mcpruntime.InstanceKey, mcpruntime.LaunchConfig, time.Duration) (mcpruntime.Instance, error) {
	return l.instance, nil
}

type blockingCloseSession struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (*blockingCloseSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}

func (s *blockingCloseSession) Close() error {
	close(s.started)
	<-s.release
	close(s.finished)
	return nil
}

func TestDiagnosticClientInstanceJoinsSessionCleanup(t *testing.T) {
	session := &blockingCloseSession{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	instance := &clientInstance{session: session, joinClose: true}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- instance.Close(ctx) }()
	select {
	case <-session.started:
	case <-time.After(time.Second):
		t.Fatal("session cleanup did not start")
	}
	select {
	case err := <-returned:
		close(session.release)
		<-session.finished
		t.Fatalf("diagnostic close returned before session cleanup: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(session.release)
	if err := <-returned; err != nil {
		t.Fatalf("joined diagnostic close failed: %v", err)
	}
	select {
	case <-session.finished:
	default:
		t.Fatal("diagnostic close returned with cleanup worker active")
	}
}

func TestDiagnosticProbeBoundsCleanupAndDiscardsToolCountOnCloseFailure(t *testing.T) {
	closed := make(chan struct{})
	probe, err := NewDiagnosticProbe(
		diagnosticValues{},
		diagnosticTokens{},
		diagnosticLauncherFixture{instance: diagnosticInstance{closed: closed, tools: []mcpruntime.Tool{{Name: "one"}, {Name: "two"}}}},
	)
	if err != nil {
		t.Fatal(err)
	}
	probe.closeTimeout = 25 * time.Millisecond
	started := time.Now()
	result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{ConnectionID: "worker", ID: "exact-revision", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: "https://worker.example/mcp"}})
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
		t.Fatalf("close failure diagnostic = %+v", result)
	}
	select {
	case <-closed:
	default:
		t.Fatal("diagnostic did not close its instance")
	}
	if elapsed := time.Since(started); elapsed > 250*time.Millisecond {
		t.Fatalf("cleanup returned after %v", elapsed)
	}
}
