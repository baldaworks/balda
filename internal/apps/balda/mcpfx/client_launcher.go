package mcpfx

import (
	"context"
	"errors"
	"net/http"
	"os/exec"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// ClientLauncher starts, initializes, and discovers tools from one exact MCP
// launch config before it can be projected into a provider runtime.
type ClientLauncher struct{}

// NewClientLauncher creates the concrete MCP client lifecycle adapter.
func NewClientLauncher() *ClientLauncher { return &ClientLauncher{} }

// Start connects and completes tools/list discovery without invoking a shell.
func (*ClientLauncher) Start(ctx context.Context, key mcpruntime.InstanceKey, config mcpruntime.LaunchConfig) (mcpruntime.Instance, error) {
	transport, err := clientTransport(config)
	if err != nil {
		return nil, err
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "balda-runtime-catalog", Version: "1"}, nil)
	session, err := client.Connect(ctx, transport, nil)
	if err != nil {
		return nil, errors.New("connect MCP server")
	}
	instance := &clientInstance{session: session}
	if err := instance.discover(ctx, key); err != nil {
		_ = session.Close()
		return nil, err
	}
	return instance, nil
}

func clientTransport(config mcpruntime.LaunchConfig) (mcp.Transport, error) {
	switch config.Transport {
	case "stdio":
		if strings.TrimSpace(config.Command) == "" {
			return nil, errors.New("MCP command is required")
		}
		command := exec.Command(config.Command, config.Args...)
		command.Dir = config.WorkingDir
		command.Env = environment(config.Env)
		return &mcp.CommandTransport{Command: command}, nil
	case "streamable-http":
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("MCP URL is required")
		}
		return &mcp.StreamableClientTransport{Endpoint: config.URL, HTTPClient: headerClient(config.Headers)}, nil
	case "sse":
		if strings.TrimSpace(config.URL) == "" {
			return nil, errors.New("MCP URL is required")
		}
		return &mcp.SSEClientTransport{Endpoint: config.URL, HTTPClient: headerClient(config.Headers)}, nil
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
	base    http.RoundTripper
	headers map[string]string
}

func (t headerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	cloned := request.Clone(request.Context())
	cloned.Header = request.Header.Clone()
	for key, value := range t.headers {
		cloned.Header.Set(key, value)
	}
	return t.base.RoundTrip(cloned)
}

func headerClient(headers map[string]string) *http.Client {
	return &http.Client{Transport: headerTransport{base: http.DefaultTransport, headers: headers}}
}

var _ mcpruntime.Launcher = (*ClientLauncher)(nil)
var _ mcpruntime.Instance = (*clientInstance)(nil)
