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
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
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

func TestDiagnosticProbeUsesTrustedTransientValues(t *testing.T) {
	const authorization = "Bearer configured-only"
	peer := mcp.NewServer(&mcp.Implementation{Name: "configured-diagnostic", Version: "1"}, nil)
	mcp.AddTool(peer, &mcp.Tool{Name: "search"}, func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{}, nil, nil
	})
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return peer }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != authorization {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	probe, err := NewDiagnosticProbe(
		diagnosticValues{err: errors.New("durable credential resolver called")},
		diagnosticTokens{},
		NewClientLauncher(),
	)
	if err != nil {
		t.Fatal(err)
	}
	result := probe.DiagnoseMCPWithValues(t.Context(), mcpcmd.Revision{
		ConnectionID: "config:knowl",
		ID:           "current-knowl",
		Definition: mcpcmd.Definition{
			Transport: mcpcmd.TransportHTTP,
			URL:       upstream.URL,
			Headers:   map[string]mcpcmd.ValueBinding{"Authorization": {Kind: mcpcmd.ValueProtected}},
		},
	}, mcpcmd.LaunchValues{Headers: map[string]string{"Authorization": authorization}})
	if result.Outcome != mcpcmd.DiagnosticSucceeded || result.ToolCount != 1 {
		t.Fatalf("configured diagnostic = %+v", result)
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
	probe.timeout = 5 * time.Second
	probe.closeTimeout = 250 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	resultCh := make(chan mcpcmd.DiagnosticResult, 1)
	go func() {
		resultCh <- probe.DiagnoseMCP(ctx, mcpcmd.Revision{
			ConnectionID: "worker",
			ID:           "exact-revision",
			Definition:   mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL},
		})
	}()
	select {
	case <-laterPage:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("later tools page did not start")
	}
	var result mcpcmd.DiagnosticResult
	select {
	case result = <-resultCh:
	case <-time.After(time.Second):
		t.Fatal("canceled diagnostic did not finish cleanup")
	}
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
		t.Fatalf("canceled discovery diagnostic = %+v", result)
	}
	for name, signal := range map[string]<-chan struct{}{
		"tools request":  pageDrained,
		"session delete": deleteDrained,
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

type cancelingCloseSession struct {
	cancel context.CancelFunc
}

func (*cancelingCloseSession) ListTools(context.Context, *mcp.ListToolsParams) (*mcp.ListToolsResult, error) {
	return &mcp.ListToolsResult{}, nil
}

func (s *cancelingCloseSession) Close() error {
	s.cancel()
	return nil
}

type blockingDiagnosticConnection struct {
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (*blockingDiagnosticConnection) Read(context.Context) (jsonrpc.Message, error) {
	return nil, io.EOF
}

func (*blockingDiagnosticConnection) Write(context.Context, jsonrpc.Message) error { return nil }

func (c *blockingDiagnosticConnection) Close() error {
	close(c.started)
	<-c.release
	close(c.finished)
	return nil
}

func (*blockingDiagnosticConnection) SessionID() string { return "diagnostic-session" }

func TestDiagnosticHTTPClientInstanceJoinsCancellationLifecycle(t *testing.T) {
	requestCtx, cancelRequest := context.WithCancel(t.Context())
	connectionCtx, cancelConnection := context.WithCancel(t.Context())
	session := &cancelingCloseSession{cancel: func() {
		cancelRequest()
		cancelConnection()
	}}
	requests := newDiagnosticRequestLifecycle()
	finishRequest := requests.begin()
	requestStarted := make(chan struct{})
	requestRelease := make(chan struct{})
	requestFinished := make(chan struct{})
	go func() {
		<-requestCtx.Done()
		close(requestStarted)
		<-requestRelease
		finishRequest()
		close(requestFinished)
	}()
	connection := &blockingDiagnosticConnection{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	connectionClose := newDiagnosticConnectionClose()
	connectionClose.bind(connectionCtx, connection)
	instance := &clientInstance{session: session, connectionClose: connectionClose, requests: requests}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	returned := make(chan error, 1)
	go func() { returned <- instance.Close(ctx) }()
	for name, started := range map[string]<-chan struct{}{"connection callback": connection.started, "request drain": requestStarted} {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatalf("%s did not start", name)
		}
	}
	select {
	case err := <-returned:
		close(connection.release)
		close(requestRelease)
		<-connection.finished
		<-requestFinished
		t.Fatalf("diagnostic close returned before cancellation cleanup: %v", err)
	default:
	}
	close(connection.release)
	<-connection.finished
	select {
	case err := <-returned:
		close(requestRelease)
		<-requestFinished
		t.Fatalf("diagnostic close returned before request drain: %v", err)
	default:
	}
	close(requestRelease)
	if err := <-returned; err != nil {
		t.Fatalf("joined diagnostic close failed: %v", err)
	}
	for name, finished := range map[string]<-chan struct{}{"connection callback": connection.finished, "request drain": requestFinished} {
		select {
		case <-finished:
		default:
			t.Fatalf("diagnostic close returned with %s active", name)
		}
	}
	if finish := requests.begin(); finish != nil {
		finish()
		t.Fatal("request lifecycle accepted work after diagnostic close")
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
