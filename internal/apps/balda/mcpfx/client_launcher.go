package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientLauncher starts, initializes, and discovers tools from one exact MCP
// launch config before it can be projected into a provider runtime.
type ClientLauncher struct{}

const transportStdio = "stdio"

const transportStreamableHTTP = "streamable-http"

const clientCloseTimeout = 5 * time.Second

const mcpCancellationMethod = "notifications/cancelled"

// NewClientLauncher creates the concrete MCP client lifecycle adapter.
func NewClientLauncher() *ClientLauncher { return &ClientLauncher{} }

// Start connects and completes tools/list discovery without invoking a shell.
func (l *ClientLauncher) Start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Instance, error) {
	return l.start(ctx, key, config, clientCloseTimeout, false)
}

// StartDiagnostic starts discovery with an explicit bound for cleanup after
// cancellation or discovery failure.
func (l *ClientLauncher) StartDiagnostic(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig, closeTimeout time.Duration) (mcpruntime.Instance, error) {
	if closeTimeout <= 0 {
		return nil, errors.New("MCP close timeout must be positive")
	}
	return l.start(ctx, key, config, closeTimeout, true)
}

func (*ClientLauncher) start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig, closeTimeout time.Duration, diagnostic bool) (mcpruntime.Instance, error) {
	observation := newLaunchObservation(config.URL)
	requestCloseTimeout := closeTimeout
	if diagnostic {
		// Leave time for the SDK to observe the canceled DELETE and finish its
		// own session close before the diagnostic cleanup bound expires.
		requestCloseTimeout /= 2
	}
	transport, err := clientTransport(config, observation, requestCloseTimeout, diagnostic)
	if err != nil {
		return nil, err
	}
	if diagnostic {
		transport = cancellationClosingTransport{Transport: transport}
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "balda-runtime-catalog", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, launchFailure(ctx, observedFailure(config, observation))
	}
	instance := &clientInstance{session: session}
	if err := instance.discover(ctx, key); err != nil {
		closeCtx, closeCancel := context.WithTimeout(context.WithoutCancel(ctx), closeTimeout)
		defer closeCancel()
		_ = instance.Close(closeCtx)
		return nil, launchFailure(ctx, observedFailure(config, observation))
	}
	return instance, nil
}

// cancellationClosingTransport ties the SDK connection lifetime to the
// diagnostic context. The SDK otherwise detaches HTTP session state from the
// connect context before initialize and tools/list complete.
type cancellationClosingTransport struct{ mcp.Transport }

func (t cancellationClosingTransport) Connect(ctx context.Context) (mcp.Connection, error) {
	connection, err := t.Transport.Connect(ctx)
	if err != nil {
		return nil, err
	}
	context.AfterFunc(ctx, func() { _ = connection.Close() })
	return connection, nil
}

func (t cancellationClosingTransport) SupportsProtocolVersion(version string) bool {
	if supporter, ok := t.Transport.(mcp.ProtocolVersionSupporter); ok {
		return supporter.SupportsProtocolVersion(version)
	}
	return true
}

func observedFailure(config mcpruntime.LaunchConfig, observation *launchObservation) func() mcpruntime.FailureReason {
	if config.ObservedFailure != nil {
		return config.ObservedFailure
	}
	return observation.failure
}

func clientTransport(config mcpruntime.LaunchConfig, observation *launchObservation, closeTimeout time.Duration, diagnostic bool) (mcp.Transport, error) {
	switch config.Transport {
	case transportStdio:
		if strings.TrimSpace(config.Command) == "" {
			return nil, errors.New("MCP command is required")
		}
		command := exec.Command(config.Command, config.Args...)
		command.Dir = config.WorkingDir
		if config.InheritEnvironment && config.WorkingDir != "" {
			directory, executable, err := resolveStdioLaunch(config.Command, config.WorkingDir)
			if err != nil {
				return nil, err
			}
			command.Dir, command.Path = directory, executable
		}
		command.Env = environment(config.Env)
		if config.InheritEnvironment {
			command.Env = append(os.Environ(), command.Env...)
		}
		return &mcp.CommandTransport{Command: command}, nil
	case transportStreamableHTTP:
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("MCP URL is required")
		}
		client, err := headerClient(config, observation, closeTimeout)
		if err != nil {
			return nil, err
		}
		transport := &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client}
		if diagnostic {
			transport.MaxRetries = -1
			transport.DisableStandaloneSSE = true
		}
		return transport, nil
	case "sse":
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("MCP URL is required")
		}
		client, err := headerClient(config, observation, closeTimeout)
		if err != nil {
			return nil, err
		}
		return &mcp.SSEClientTransport{Endpoint: config.URL, HTTPClient: client}, nil
	default:
		return nil, errors.New("MCP transport is unsupported")
	}
}

type clientInstance struct {
	session *mcp.ClientSession
	tools   []mcpruntime.Tool
}

func (i *clientInstance) discover(ctx context.Context, key mcpruntime.InstanceKey) error {
	params := &mcp.ListToolsParams{}
	for {
		result, err := i.session.ListTools(ctx, params)
		if err != nil {
			return errors.New("discover MCP tools")
		}
		for _, tool := range result.Tools {
			if tool == nil {
				continue
			}
			i.tools = append(i.tools, mcpruntime.Tool{Key: key, Name: tool.Name, Description: tool.Description})
		}
		if result.NextCursor == "" {
			break
		}
		params.Cursor = result.NextCursor
	}
	sort.Slice(i.tools, func(left, right int) bool { return i.tools[left].Name < i.tools[right].Name })
	return nil
}

func (i *clientInstance) Tools() []mcpruntime.Tool {
	return append([]mcpruntime.Tool(nil), i.tools...)
}

func (i *clientInstance) Close(ctx context.Context) error {
	if i == nil || i.session == nil {
		return nil
	}
	done := make(chan error, 1)
	go func() { done <- i.session.Close() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func environment(values map[string]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		env = append(env, key+"="+values[key])
	}
	return env
}

type headerTransport struct {
	base         http.RoundTripper
	headers      map[string]string
	origin       *url.URL
	observation  *launchObservation
	closeTimeout time.Duration
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	// SDK session shutdown and cancellation notifications use detached
	// contexts. Bound both without limiting healthy MCP requests.
	if request.Method == http.MethodDelete || isMCPCancellation(request) {
		ctx, cancel := context.WithTimeout(request.Context(), t.closeTimeout)
		defer cancel()
		request = request.Clone(ctx)
	}
	// SSE endpoint events can create new requests without a redirect. Check
	// every destination before either inherited or configured headers are sent.
	if t.origin != nil && !sameHTTPOrigin(request.URL, t.origin) {
		return nil, errors.New("MCP request changes origin")
	}
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	response, err := t.base.RoundTrip(cloned)
	// Only a response from the original configured origin is evidence. For
	// bridge projections the separate upstream observation owns this decision.
	if t.observation != nil && t.observation.origin != nil && sameHTTPOrigin(request.URL, t.observation.origin) {
		if err != nil {
			t.observation.observe(0, nil, err)
		} else {
			t.observation.observe(response.StatusCode, response.Header, nil)
		}
	}
	return response, err
}

func isMCPCancellation(request *http.Request) bool {
	if request.Method != http.MethodPost || request.GetBody == nil {
		return false
	}
	body, err := request.GetBody()
	if err != nil {
		return false
	}
	defer func() { _ = body.Close() }()
	var message struct {
		Method string `json:"method"`
	}
	return json.NewDecoder(io.LimitReader(body, 4<<10)).Decode(&message) == nil && message.Method == mcpCancellationMethod
}

func sameHTTPOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func headerClient(config mcpruntime.LaunchConfig, observation *launchObservation, closeTimeout time.Duration) (*http.Client, error) {
	transport := headerTransport{base: http.DefaultTransport, headers: config.Headers, observation: observation, closeTimeout: closeTimeout}
	client := &http.Client{}
	if config.EnforceHTTPOrigin {
		origin, err := url.Parse(config.URL)
		if err != nil || origin.Host == "" || origin.User != nil || (origin.Scheme != "http" && origin.Scheme != "https") {
			return nil, errors.New("MCP credential origin is invalid")
		}
		transport.origin = origin
		client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
			if len(via) == 0 || len(via) >= 10 {
				return errors.New("MCP redirect limit exceeded")
			}
			if !sameHTTPOrigin(request.URL, origin) {
				return errors.New("MCP redirect changes origin")
			}
			return nil
		}
	}
	client.Transport = transport
	return client, nil
}

var _ mcpruntime.Launcher = (*ClientLauncher)(nil)
var _ mcpruntime.Instance = (*clientInstance)(nil)
