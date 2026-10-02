package mcpbridge

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestStaticBridgePreservesRealHTTPAndSSEToolSessions(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "fixed-upstream", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct{ Text string }) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
			})
			var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			if transport == mcpcmd.TransportSSE {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("X-Worker-Secret") != "static-worker-secret" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("X-Balda-MCP-Capability") != "" {
					t.Error("bridge leaked local credentials or omitted fixed headers")
				}
				handler.ServeHTTP(w, r)
			}))
			defer upstream.Close()
			bridge := New(nil, upstream.Client().Transport)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				if err := bridge.Close(ctx); err != nil {
					t.Error(err)
				}
			}()
			projection, err := bridge.Install("static-revision", Endpoint{URL: upstream.URL + "/mcp", Transport: transport, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
			if err != nil {
				t.Fatalf("static bridge installation failed: %v", err)
			}
			if strings.Contains(projection.URL, "static-worker-secret") {
				t.Fatal("upstream secret entered provider endpoint")
			}
			client := &http.Client{Transport: bridgeTestHeaders{base: http.DefaultTransport, headers: projection.Headers}}
			var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: projection.URL, HTTPClient: client}
			if transport == mcpcmd.TransportSSE {
				wire = &mcp.SSEClientTransport{Endpoint: projection.URL, HTTPClient: client}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "bridge-contract", Version: "1"}, nil).Connect(ctx, wire, nil)
			if err != nil {
				t.Fatalf("actual SDK initialize failed: %v", err)
			}
			defer func() { _ = session.Close() }()
			tools, err := session.ListTools(ctx, &mcp.ListToolsParams{})
			if err != nil || len(tools.Tools) != 1 || tools.Tools[0].Name != "echo" {
				t.Fatalf("actual SDK tools/list failed: %v", err)
			}
			result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]string{"Text": "through-fixed-upstream"}})
			if err != nil || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "through-fixed-upstream" {
				t.Fatalf("actual SDK invocation failed: %v", err)
			}
			large := strings.Repeat("large-result-", 16<<10)
			result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]string{"Text": large}})
			if err != nil || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != large {
				t.Fatalf("bridge truncated or buffered away a large streamed result: %v", err)
			}
		})
	}
}

type gzipSSEWriter struct {
	http.ResponseWriter
	writer *gzip.Writer
}

func (w gzipSSEWriter) Write(data []byte) (int, error) { return w.writer.Write(data) }
func (w gzipSSEWriter) Flush()                         { _ = w.writer.Flush(); w.ResponseWriter.(http.Flusher).Flush() }

func TestBridgeSupportsCompressedLegacySSEWithoutExposingUpstreamEndpoint(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "compressed-sse", Version: "1"}, nil)
	mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct{ Text string }) (*mcp.CallToolResult, any, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
	})
	handler := mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Encoding", "gzip")
			writer := gzip.NewWriter(w)
			defer func() { _ = writer.Close() }()
			handler.ServeHTTP(gzipSSEWriter{ResponseWriter: w, writer: writer}, r)
			return
		}
		handler.ServeHTTP(w, r)
	}))
	defer upstream.Close()
	bridge := New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bridge.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	projection, err := bridge.Install("compressed", Endpoint{URL: upstream.URL + "/sse", Transport: mcpcmd.TransportSSE, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: bridgeTestHeaders{base: http.DefaultTransport, headers: projection.Headers}}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "compressed-client", Version: "1"}, nil).Connect(ctx, &mcp.SSEClientTransport{Endpoint: projection.URL, HTTPClient: client}, nil)
	if err != nil {
		t.Fatalf("compressed legacy endpoint cannot initialize: %v", err)
	}
	defer func() { _ = session.Close() }()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]string{"Text": "compressed-roundtrip"}})
	if err != nil || len(result.Content) != 1 || result.Content[0].(*mcp.TextContent).Text != "compressed-roundtrip" {
		t.Fatalf("compressed legacy SSE tool result changed: %v", err)
	}
}

func TestBridgePreservesMethodsSessionHeadersAndFixedQueryWithoutReplay(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/fixed" || r.URL.RawQuery != "resource=fixed" || r.Header.Get("Mcp-Session-Id") != "retained-session" || r.Header.Get("Mcp-Protocol-Version") != "2025-11-25" || r.Header.Get("Last-Event-Id") != "event-7" || r.Header.Get(CapabilityHeader) != "" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" || r.Header.Get("Proxy-Authorization") != "" {
			t.Error("fixed upstream/session/protocol/caller-credential contract changed")
		}
		w.Header().Set("Mcp-Session-Id", "retained-session")
		w.Header().Set("Set-Cookie", "upstream=private")
		if r.Header.Get("X-Reject-Tool") == "true" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(w, "accepted")
	}))
	defer upstream.Close()
	bridge := New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bridge.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	projection, err := bridge.Install("protocol", Endpoint{URL: upstream.URL + "/fixed?resource=fixed", Transport: mcpcmd.TransportHTTP, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete, http.MethodPost} {
		r, err := http.NewRequestWithContext(t.Context(), method, projection.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(CapabilityHeader, projection.Headers[CapabilityHeader])
		r.Header.Set("Authorization", "Bearer caller-private")
		r.Header.Set("Proxy-Authorization", "Basic caller-private")
		r.Header.Set("Cookie", "caller=private")
		r.Header.Set("Mcp-Session-Id", "retained-session")
		r.Header.Set("Mcp-Protocol-Version", "2025-11-25")
		r.Header.Set("Last-Event-Id", "event-7")
		want := http.StatusAccepted
		if calls.Load() == 3 {
			r.Header.Set("X-Reject-Tool", "true")
			want = http.StatusUnauthorized
		}
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		if response.StatusCode != want || response.Header.Get("Mcp-Session-Id") != "retained-session" || response.Header.Get("Set-Cookie") != "" {
			t.Fatal("HTTP status/session headers changed or upstream cookie escaped")
		}
	}
	if calls.Load() != 4 {
		t.Fatal("bridge replayed an ambiguous rejected tool POST")
	}
}

func TestBridgeShutdownCancelsLegacyStreamAndInvalidatesAliases(t *testing.T) {
	streaming, cancelled := make(chan struct{}), make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "id: initial\nretry: 1000\nevent: endpoint\ndata: ?sessionid=private-upstream-session\n\n")
		w.(http.Flusher).Flush()
		close(streaming)
		<-r.Context().Done()
		close(cancelled)
	}))
	defer upstream.Close()
	bridge := New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	projection, err := bridge.Install("legacy-stream", Endpoint{URL: upstream.URL + "/sse", Transport: mcpcmd.TransportSSE, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: bridgeTestHeaders{base: http.DefaultTransport, headers: projection.Headers}}
	response, err := client.Get(projection.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = response.Body.Close() }()
	<-streaming
	reader := bufio.NewReader(response.Body)
	var initial strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		initial.WriteString(line)
		if line == "\n" {
			break
		}
	}
	if !strings.Contains(initial.String(), "id: initial") || !strings.Contains(initial.String(), "retry: 1000") || strings.Contains(initial.String(), "private-upstream-session") {
		t.Fatal("endpoint rewrite lost event metadata or exposed upstream session routing")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := bridge.Close(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not cancel upstream stream")
	}
	if _, err := bridge.Install("after-close", Endpoint{URL: upstream.URL, Transport: mcpcmd.TransportSSE}); !errors.Is(err, mcpcmd.ErrUnavailable) {
		t.Fatal("stopped bridge installed another endpoint")
	}
}

type bridgeTokens struct {
	mu      sync.Mutex
	token   string
	err     error
	binding mcpcmd.AuthBinding
	t       *testing.T
}

func (s *bridgeTokens) AccessToken(_ context.Context, binding mcpcmd.AuthBinding, scopes []string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if binding != s.binding || len(scopes) != 1 || scopes[0] != "tools" {
		s.t.Error("bridge requested an unbound worker token")
	}
	return s.token, s.err
}

func TestOAuthBridgeRenewsInsideStableHTTPAndSSESessionAndDisconnects(t *testing.T) {
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			server := mcp.NewServer(&mcp.Implementation{Name: "worker-oauth", Version: "1"}, nil)
			mcp.AddTool(server, &mcp.Tool{Name: "echo"}, func(_ context.Context, _ *mcp.CallToolRequest, args struct{ Text string }) (*mcp.CallToolResult, any, error) {
				return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: args.Text}}}, nil, nil
			})
			var handler http.Handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			if transport == mcpcmd.TransportSSE {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			toolTokens := make(chan string, 4)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(CapabilityHeader) != "" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Worker-Secret") != "static-worker-secret" {
					t.Error("upstream received a local credential or lost bound static header")
				}
				if r.Method == http.MethodPost {
					body, err := io.ReadAll(r.Body)
					if err != nil {
						t.Error(err)
					}
					r.Body = io.NopCloser(bytes.NewReader(body))
					var request struct{ Method string }
					_ = json.Unmarshal(body, &request)
					if request.Method == "tools/call" {
						toolTokens <- r.Header.Get("Authorization")
					}
				}
				handler.ServeHTTP(w, r)
			}))
			defer upstream.Close()
			binding := mcpcmd.AuthBinding{ConnectionID: "worker", Resource: upstream.URL + "/mcp", Issuer: "https://issuer.example.org", ClientID: "worker-client"}
			tokens := &bridgeTokens{token: "initial-worker-access", binding: binding, t: t}
			bridge := New(tokens, upstream.Client().Transport)
			if err := bridge.Start(t.Context()); err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := bridge.Close(t.Context()); err != nil {
					t.Error(err)
				}
			}()
			config := Endpoint{URL: binding.Resource, Transport: transport, Binding: &binding, Scopes: []string{"tools"}, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}}
			projection, err := bridge.Install("oauth-revision", config)
			if err != nil {
				t.Fatal(err)
			}
			client := &http.Client{Transport: bridgeTestHeaders{base: http.DefaultTransport, headers: projection.Headers}}
			var wire mcp.Transport = &mcp.StreamableClientTransport{Endpoint: projection.URL, HTTPClient: client}
			if transport == mcpcmd.TransportSSE {
				wire = &mcp.SSEClientTransport{Endpoint: projection.URL, HTTPClient: client}
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			session, err := mcp.NewClient(&mcp.Implementation{Name: "bound-worker", Version: "1"}, nil).Connect(ctx, wire, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = session.Close() }()
			for _, token := range []string{"initial-worker-access", "renewed-worker-access"} {
				tokens.mu.Lock()
				tokens.token = token
				tokens.mu.Unlock()
				if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]string{"Text": "same-session"}}); err != nil {
					t.Fatal(err)
				}
				if received := <-toolTokens; received != "Bearer "+token {
					t.Fatalf("existing session kept old worker credentials: %s", received)
				}
				same, err := bridge.Install("oauth-revision", config)
				if err != nil || same.URL != projection.URL || same.Headers[CapabilityHeader] != projection.Headers[CapabilityHeader] {
					t.Fatal("token renewal changed the exact revision endpoint")
				}
			}
			tokens.mu.Lock()
			tokens.err = mcpcmd.ErrDisconnected
			tokens.mu.Unlock()
			if _, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "echo", Arguments: map[string]string{"Text": "after-disconnect"}}); err == nil {
				t.Fatal("disconnected session invoked upstream")
			}
			select {
			case <-toolTokens:
				t.Fatal("disconnected grant reached remote tool")
			default:
			}
		})
	}
}

func TestBridgeRejectsLocalBoundaryViolationsBeforeUpstream(t *testing.T) {
	var calls atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { calls.Add(1); w.WriteHeader(http.StatusNoContent) }))
	defer upstream.Close()
	bridge := New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bridge.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	projection, err := bridge.Install("guarded", Endpoint{URL: upstream.URL + "/fixed", Transport: mcpcmd.TransportHTTP, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*http.Request){
		func(r *http.Request) { r.Header.Del(CapabilityHeader) },
		func(r *http.Request) { r.Header.Set(CapabilityHeader, "wrong-capability") },
		func(r *http.Request) { r.Header.Add(CapabilityHeader, r.Header.Get(CapabilityHeader)) },
		func(r *http.Request) { r.Host = "foreign.example.org" },
		func(r *http.Request) { r.Header.Set("Origin", "https://foreign.example.org") },
		func(r *http.Request) { r.URL.RawQuery = "target=https%3A%2F%2Fforeign.example.org" },
	} {
		r, err := http.NewRequestWithContext(t.Context(), http.MethodPost, projection.URL, strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(CapabilityHeader, projection.Headers[CapabilityHeader])
		change(r)
		response, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode < 400 {
			t.Fatalf("unsafe local request accepted: %d", response.StatusCode)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid local request touched upstream")
	}
	bridge.Remove("guarded")
	r, err := http.NewRequestWithContext(t.Context(), http.MethodGet, projection.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	r.Header.Set(CapabilityHeader, projection.Headers[CapabilityHeader])
	response, err := http.DefaultClient.Do(r)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatal("removed endpoint remained accessible")
	}
}

func TestBridgeForeignRedirectAndSSEEndpointCannotReceiveCredentials(t *testing.T) {
	var foreignCalls atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { foreignCalls.Add(1); w.WriteHeader(http.StatusOK) }))
	defer foreign.Close()
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if transport == mcpcmd.TransportHTTP {
				http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(w, "event: endpoint\ndata: "+foreign.URL+"/messages\n\n")
		}))
		bridge := New(nil, nil)
		if err := bridge.Start(t.Context()); err != nil {
			t.Fatal(err)
		}
		projection, err := bridge.Install("foreign-boundary", Endpoint{URL: upstream.URL, Transport: transport, Headers: map[string]string{"X-Worker-Secret": "static-worker-secret"}})
		if err != nil {
			t.Fatal(err)
		}
		client := &http.Client{Transport: bridgeTestHeaders{base: http.DefaultTransport, headers: projection.Headers}}
		response, err := client.Get(projection.URL)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if response.StatusCode != http.StatusBadGateway || strings.Contains(string(body), "static-worker-secret") || strings.Contains(string(body), projection.Headers[CapabilityHeader]) {
			t.Fatal("foreign destination or credential disclosed to caller")
		}
		_ = bridge.Close(t.Context())
		upstream.Close()
	}
	if foreignCalls.Load() != 0 {
		t.Fatal("foreign redirect or endpoint received a bridge request")
	}
}

func TestBridgeRejectsUnboundOAuthIdentityAndStaticAuthorizationConflict(t *testing.T) {
	binding := mcpcmd.AuthBinding{ConnectionID: "worker", Resource: "https://resource.example.org/mcp", Issuer: "https://issuer.example.org", ClientID: "worker-client"}
	bridge := New(&bridgeTokens{binding: binding, t: t}, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := bridge.Close(t.Context()); err != nil {
			t.Error(err)
		}
	}()
	if _, err := bridge.Install("wrong-resource", Endpoint{URL: "https://other-resource.example.org/mcp", Transport: mcpcmd.TransportHTTP, Binding: &binding}); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatal("worker token bound to a different resource")
	}
	if _, err := bridge.Install("static-oauth-conflict", Endpoint{URL: binding.Resource, Transport: mcpcmd.TransportHTTP, Binding: &binding, Headers: map[string]string{"Authorization": "static-worker-secret"}}); !errors.Is(err, mcpcmd.ErrConflict) {
		t.Fatalf("OAuth accepted competing static Authorization: %v", err)
	}
}

type bridgeTestHeaders struct {
	base    http.RoundTripper
	headers map[string]string
}

func (t bridgeTestHeaders) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header = r.Header.Clone()
	for k, v := range t.headers {
		r.Header.Set(k, v)
	}
	r.Header.Set("Cookie", "browser=private-cookie")
	return t.base.RoundTrip(r)
}
