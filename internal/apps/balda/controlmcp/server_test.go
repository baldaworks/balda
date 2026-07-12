package controlmcp

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"go.uber.org/fx"
)

type fakeShutdowner struct {
	calls int
	err   error
}

func (f *fakeShutdowner) Shutdown(...fx.ShutdownOption) error {
	f.calls++
	return f.err
}

func TestRegisterToolsListsShutdownTool(t *testing.T) {
	ctx, cleanup, session := newTestSession(t, &fakeShutdowner{}, nil, nil)
	defer cleanup()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}

	found := false
	for _, tool := range tools.Tools {
		if tool.Name == "balda.control.shutdown" {
			found = true
			if tool.Description == "" {
				t.Fatal("balda.control.shutdown description is empty")
			}
		}
	}
	if !found {
		t.Fatal("balda.control.shutdown tool missing")
	}
}

func TestShutdownRequiresExplicitConfirmation(t *testing.T) {
	ctx, cleanup, session := newTestSession(t, &fakeShutdowner{}, nil, nil)
	defer cleanup()

	result := callTool(t, ctx, session, "balda.control.shutdown", map[string]any{"confirm": "yes"})
	if !result.IsError {
		t.Fatal("result.IsError = false, want true")
	}
	payload := structuredResultMap(t, result)
	errObj := payload["error"].(map[string]any)
	if errObj["code"] != codeValidationError {
		t.Fatalf("error.code = %v, want %q", errObj["code"], codeValidationError)
	}
}

func TestShutdownRequestsGracefulStop(t *testing.T) {
	shutdowner := &fakeShutdowner{}
	terminator := &fakeTerminator{called: make(chan struct{}, 1)}
	ctx, cleanup, session := newTestSession(t, shutdowner, nil, terminator.call)
	defer cleanup()

	result := callTool(t, ctx, session, "balda.control.shutdown", map[string]any{
		"confirm": "shutdown",
		"reason":  "restart after deploy",
	})
	if result.IsError {
		t.Fatal("result.IsError = true, want false")
	}
	payload := structuredResultMap(t, result)
	if payload["requested"] != true {
		t.Fatalf("requested = %v, want true", payload["requested"])
	}
	if shutdowner.calls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", shutdowner.calls)
	}
	select {
	case <-terminator.called:
	case <-time.After(2 * time.Second):
		t.Fatal("terminator was not called")
	}
	if terminator.calls != 1 {
		t.Fatalf("terminator calls = %d, want 1", terminator.calls)
	}
}

func TestShutdownDoesNotTerminateProcessWhenShutdownFails(t *testing.T) {
	shutdowner := &fakeShutdowner{err: errors.New("boom")}
	terminator := &fakeTerminator{called: make(chan struct{}, 1)}
	ctx, cleanup, session := newTestSession(t, shutdowner, nil, terminator.call)
	defer cleanup()

	result := callTool(t, ctx, session, "balda.control.shutdown", map[string]any{
		"confirm": "shutdown",
	})
	if !result.IsError {
		t.Fatal("result.IsError = false, want true")
	}
	if shutdowner.calls != 1 {
		t.Fatalf("shutdown calls = %d, want 1", shutdowner.calls)
	}
	select {
	case <-terminator.called:
		t.Fatal("terminator called unexpectedly")
	case <-time.After(200 * time.Millisecond):
	}
}

type fakeTerminator struct {
	calls  int
	called chan struct{}
}

func (f *fakeTerminator) call() error {
	f.calls++
	if f.called != nil {
		f.called <- struct{}{}
	}
	return nil
}

func newTestSession(t *testing.T, shutdowner fx.Shutdowner, dispatcher actortransport.Dispatcher, terminate func() error) (context.Context, func(), *mcp.ClientSession) {
	t.Helper()
	server := mcp.NewServer(
		&mcp.Implementation{Name: "test-control", Version: "1.0.0"},
		nil,
	)
	registerTools(server, shutdowner, dispatcher, terminate)

	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		_ = server.Run(ctx, serverTransport)
	}()

	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "1.0.0"}, nil)
	session, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		t.Fatalf("client.Connect() error = %v", err)
	}
	cleanup := func() {
		cancel()
		_ = session.Close()
	}
	return ctx, cleanup, session
}

func callTool(t *testing.T, ctx context.Context, session *mcp.ClientSession, toolName string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: toolName, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s) error = %v", toolName, err)
	}
	return result
}

func structuredResultMap(t *testing.T, result *mcp.CallToolResult) map[string]any {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	switch typed := result.StructuredContent.(type) {
	case map[string]any:
		return typed
	case json.RawMessage:
		var decoded map[string]any
		if err := json.Unmarshal(typed, &decoded); err != nil {
			t.Fatalf("json.Unmarshal(structured content) error = %v", err)
		}
		return decoded
	default:
		t.Fatalf("unexpected structured content type %T", result.StructuredContent)
	}
	return nil
}
