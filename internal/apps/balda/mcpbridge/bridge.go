// Package mcpbridge owns fixed-upstream credential-scoped MCP transport.
package mcpbridge

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

// Credentials provides only the current token for an exact worker identity.
type Credentials interface {
	AccessToken(ctx context.Context, binding mcpcmd.AuthBinding, scopes []string) (string, error)
}

// Endpoint is private resolved launch input, retained for one exact revision.
type Endpoint struct {
	URL       string
	Transport mcpcmd.Transport
	Headers   map[string]string `json:"-"`
	Binding   *mcpcmd.AuthBinding
	Scopes    []string
	// Observe receives only upstream responses or worker credential failures,
	// never an incoming loopback response. It does not change forwarding.
	Observe func(status int, headers http.Header, err error) `json:"-"`
}

// Projection carries process-local provider access, never a public snapshot.
type Projection struct {
	URL     string            `json:"-"`
	Headers map[string]string `json:"-"`
}

// CapabilityHeader authorizes only this installation's loopback transport.
const CapabilityHeader = "X-Balda-MCP-Capability"
const maxRoutes = 4096

type endpoint struct {
	id, path, capability string
	config               Endpoint
	target               *url.URL
	ctx                  context.Context
	cancel               context.CancelFunc
}
type route struct {
	endpoint *endpoint
	target   *url.URL
	message  bool
}

// Bridge serves credential-bearing connections on a private loopback listener.
type Bridge struct {
	mu          sync.RWMutex
	credentials Credentials
	transport   http.RoundTripper
	entries     map[string]*endpoint
	routes      map[string]route
	ctx         context.Context
	cancel      context.CancelFunc
	server      *http.Server
	address     string
	stopped     bool
	serveDone   chan struct{}
}

// New constructs an installation-local bridge without opening a listener.
func New(credentials Credentials, transport http.RoundTripper) *Bridge {
	if transport == nil {
		transport = http.DefaultTransport
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Bridge{credentials: credentials, transport: transport, entries: make(map[string]*endpoint), routes: make(map[string]route), ctx: ctx, cancel: cancel}
}

// Start binds the listener before provider execution.
func (b *Bridge) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped {
		return mcpcmd.ErrUnavailable
	}
	if b.server != nil {
		return nil
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return mcpcmd.ErrUnavailable
	}
	b.address = listener.Addr().String()
	b.server = &http.Server{Handler: b, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 32 << 10, BaseContext: func(net.Listener) context.Context { return b.ctx }, ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn)}
	b.serveDone = make(chan struct{})
	go func() { defer close(b.serveDone); _ = b.server.Serve(listener) }()
	return nil
}

// Close cancels in-flight requests and drains the owned listener.
func (b *Bridge) Close(ctx context.Context) error {
	b.mu.Lock()
	b.stopped = true
	b.cancel()
	for _, e := range b.entries {
		e.cancel()
	}
	clear(b.entries)
	clear(b.routes)
	server, done := b.server, b.serveDone
	b.mu.Unlock()
	if server == nil {
		return nil
	}
	err := server.Shutdown(ctx)
	if err != nil {
		_ = server.Close()
	}
	select {
	case <-done:
	case <-ctx.Done():
		return ctx.Err()
	}
	return err
}

// Install creates stable private transport access for one immutable launch ID.
func (b *Bridge) Install(id string, config Endpoint) (Projection, error) {
	target, err := validateEndpoint(config, b.credentials != nil)
	if err != nil {
		return Projection{}, err
	}
	if id == "" {
		return Projection{}, mcpcmd.ErrInvalid
	}
	config.Headers = maps.Clone(config.Headers)
	config.Scopes = slices.Clone(config.Scopes)
	if config.Binding != nil {
		binding := *config.Binding
		config.Binding = &binding
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.stopped || b.server == nil {
		return Projection{}, mcpcmd.ErrUnavailable
	}
	if existing := b.entries[id]; existing != nil {
		if !sameEndpoint(existing.config, config) {
			return Projection{}, mcpcmd.ErrConflict
		}
		return b.projection(existing), nil
	}
	if len(b.routes) >= maxRoutes {
		return Projection{}, mcpcmd.ErrUnavailable
	}
	ctx, cancel := context.WithCancel(b.ctx)
	e := &endpoint{id: id, path: "/mcp/" + rand.Text(), capability: rand.Text(), config: config, target: target, ctx: ctx, cancel: cancel}
	b.entries[id] = e
	b.routes[e.path] = route{endpoint: e, target: target}
	return b.projection(e), nil
}

func (b *Bridge) projection(e *endpoint) Projection {
	return Projection{URL: "http://" + b.address + e.path, Headers: map[string]string{CapabilityHeader: e.capability}}
}

// Remove invalidates access after the exact revision has drained.
func (b *Bridge) Remove(id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	e := b.entries[id]
	if e == nil {
		return
	}
	delete(b.entries, id)
	e.cancel()
	for path, r := range b.routes {
		if r.endpoint == e {
			delete(b.routes, path)
		}
	}
}

// ServeHTTP admits local capability-bound requests before touching credentials.
func (b *Bridge) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	b.mu.RLock()
	selected, found := b.routes[r.URL.Path]
	address, stopped := b.address, b.stopped
	b.mu.RUnlock()
	if stopped {
		http.Error(w, "MCP bridge unavailable", http.StatusServiceUnavailable)
		return
	}
	if !found {
		http.NotFound(w, r)
		return
	}
	e := selected.endpoint
	origin := r.Header.Get("Origin")
	if r.Host != address || (origin != "" && origin != "http://"+address) || len(r.Header.Values(CapabilityHeader)) != 1 || subtle.ConstantTimeCompare([]byte(r.Header.Get(CapabilityHeader)), []byte(e.capability)) != 1 {
		http.Error(w, "MCP bridge access denied", http.StatusForbidden)
		return
	}
	if r.URL.RawQuery != "" || r.URL.RawPath != "" {
		http.Error(w, "MCP bridge route invalid", http.StatusBadRequest)
		return
	}
	if selected.message && r.Method != http.MethodPost || !selected.message && e.config.Transport == mcpcmd.TransportSSE && r.Method != http.MethodGet || e.config.Transport == mcpcmd.TransportHTTP && r.Method != http.MethodGet && r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "MCP bridge method unavailable", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	stop := context.AfterFunc(e.ctx, cancel)
	defer stop()
	defer cancel()
	if e.ctx.Err() != nil {
		http.Error(w, "MCP bridge unavailable", http.StatusServiceUnavailable)
		return
	}
	r = r.Clone(ctx)
	headers := maps.Clone(e.config.Headers)
	if e.config.Binding != nil {
		token, err := b.credentials.AccessToken(ctx, *e.config.Binding, e.config.Scopes)
		if err != nil || token == "" || len(token) > 16<<10 || strings.ContainsAny(token, "\x00\r\n") {
			if e.config.Observe != nil {
				e.config.Observe(0, nil, err)
			}
			status := http.StatusBadGateway
			if errors.Is(err, mcpcmd.ErrAuthRequired) || errors.Is(err, mcpcmd.ErrDisconnected) {
				status = http.StatusUnauthorized
			}
			http.Error(w, "MCP worker authorization unavailable", status)
			return
		}
		if headers == nil {
			headers = make(map[string]string)
		}
		headers["Authorization"] = "Bearer " + token
	}
	proxy := httputil.ReverseProxy{
		Rewrite: func(p *httputil.ProxyRequest) {
			target := *selected.target
			p.Out.URL = &target
			p.Out.Host = target.Host
			p.Out.GetBody = nil
			p.Out.Trailer = nil
			for _, name := range []string{"Authorization", "Proxy-Authorization", "Cookie", "Cookie2", CapabilityHeader, "Origin", "Referer", "Forwarded", "X-Forwarded-For", "X-Forwarded-Host", "X-Forwarded-Proto"} {
				p.Out.Header.Del(name)
			}
			for name, value := range headers {
				p.Out.Header.Set(name, value)
			}
		},
		Transport: upstreamTransport{base: b.transport, observe: e.config.Observe}, FlushInterval: -1,
		ErrorLog: slog.NewLogLogger(slog.Default().Handler(), slog.LevelWarn),
		ErrorHandler: func(w http.ResponseWriter, _ *http.Request, _ error) {
			http.Error(w, "MCP upstream unavailable", http.StatusBadGateway)
		},
		ModifyResponse: func(response *http.Response) error {
			if response.StatusCode >= 300 && response.StatusCode < 400 {
				return mcpcmd.ErrUnavailable
			}
			response.Header.Del("Set-Cookie")
			response.Header.Del(CapabilityHeader)
			if e.config.Transport == mcpcmd.TransportSSE && !selected.message && response.StatusCode >= 200 && response.StatusCode < 300 {
				return b.rewriteEndpoint(e, response)
			}
			return nil
		},
	}
	proxy.ServeHTTP(w, r)
}

type upstreamTransport struct {
	base    http.RoundTripper
	observe func(int, http.Header, error)
}

func (t upstreamTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	response, err := t.base.RoundTrip(r)
	if t.observe != nil {
		if err != nil {
			t.observe(0, nil, err)
		} else {
			t.observe(response.StatusCode, response.Header, nil)
		}
	}
	if err != nil {
		return nil, mcpcmd.ErrUnavailable
	}
	return response, nil
}

func sameEndpoint(a, b Endpoint) bool {
	if a.URL != b.URL || a.Transport != b.Transport || !maps.Equal(a.Headers, b.Headers) || !slices.Equal(a.Scopes, b.Scopes) || (a.Binding == nil) != (b.Binding == nil) {
		return false
	}
	return a.Binding == nil || *a.Binding == *b.Binding
}

func validateEndpoint(config Endpoint, credentials bool) (*url.URL, error) {
	target, err := url.Parse(config.URL)
	if err != nil || target.Hostname() == "" || target.User != nil || target.Fragment != "" || target.Opaque != "" || (target.Scheme != "http" && target.Scheme != "https") || (config.Transport != mcpcmd.TransportHTTP && config.Transport != mcpcmd.TransportSSE) {
		return nil, mcpcmd.ErrInvalid
	}
	if config.Binding != nil {
		binding := *config.Binding
		if !credentials || binding.ConnectionID == "" || binding.ClientID == "" || binding.Issuer == "" || binding.Resource != config.URL {
			return nil, mcpcmd.ErrInvalid
		}
	}
	seen := make(map[string]bool)
	for name, value := range config.Headers {
		key := http.CanonicalHeaderKey(name)
		if name == "" || seen[key] || len(value) > 16<<10 || strings.ContainsAny(value, "\x00\r\n") {
			return nil, mcpcmd.ErrInvalid
		}
		for _, c := range name {
			allowed := c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", c)
			if !allowed {
				return nil, mcpcmd.ErrInvalid
			}
		}
		seen[key] = true
		switch key {
		case "Host", "Origin", "Connection", "Proxy-Authorization", "Proxy-Connection", "Transfer-Encoding", "Content-Length", "Upgrade", "Trailer", "Te", CapabilityHeader, "Mcp-Session-Id", "Mcp-Protocol-Version":
			return nil, mcpcmd.ErrInvalid
		}
		if key == "Authorization" && config.Binding != nil {
			return nil, mcpcmd.ErrConflict
		}
	}
	return target, nil
}
