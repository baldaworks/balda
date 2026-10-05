package mcpfx

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientLauncher starts, initializes, and discovers tools from one exact MCP
// launch config before it can be projected into a provider runtime.
type ClientLauncher struct{}

const transportStdio = "stdio"

const transportStreamableHTTP = "streamable-http"

// NewClientLauncher creates the concrete MCP client lifecycle adapter.
func NewClientLauncher() *ClientLauncher { return &ClientLauncher{} }

// Start connects and completes tools/list discovery without invoking a shell.
func (*ClientLauncher) Start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Instance, error) {
	observation := newLaunchObservation(config.URL)
	transport, err := clientTransport(config, observation)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "balda-runtime-catalog", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, launchFailure(ctx, observedFailure(config, observation))
	}
	instance := &clientInstance{session: session}
	if err := instance.discover(ctx, key); err != nil {
		_ = session.Close()
		return nil, launchFailure(ctx, observedFailure(config, observation))
	}
	return instance, nil
}

func observedFailure(config mcpruntime.LaunchConfig, observation *launchObservation) func() mcpruntime.FailureReason {
	if config.ObservedFailure != nil {
		return config.ObservedFailure
	}
	return observation.failure
}

func clientTransport(config mcpruntime.LaunchConfig, observation *launchObservation) (mcp.Transport, error) {
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
		client, err := headerClient(config, observation)
		if err != nil {
			return nil, err
		}
		return &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: client}, nil
	case "sse":
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("MCP URL is required")
		}
		client, err := headerClient(config, observation)
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
	base        http.RoundTripper
	headers     map[string]string
	origin      *url.URL
	observation *launchObservation
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
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

func sameHTTPOrigin(left, right *url.URL) bool {
	return strings.EqualFold(left.Scheme, right.Scheme) && strings.EqualFold(left.Host, right.Host)
}

func headerClient(config mcpruntime.LaunchConfig, observation *launchObservation) (*http.Client, error) {
	transport := headerTransport{base: http.DefaultTransport, headers: config.Headers, observation: observation}
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
