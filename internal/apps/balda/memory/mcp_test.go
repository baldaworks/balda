package memory

import (
	"context"
	"slices"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestRegisterToolsSkipsDisabledMemory(t *testing.T) {
	t.Parallel()

	ctx, cleanup, session := newMemoryMCPTestSession(t, NewStore(t.TempDir(), false))
	defer cleanup()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	if len(tools.Tools) != 0 {
		t.Fatalf("ListTools() returned %d tools, want 0", len(tools.Tools))
	}
}

func TestRegisterToolsAddsMemoryToolsWhenEnabled(t *testing.T) {
	t.Parallel()

	ctx, cleanup, session := newMemoryMCPTestSession(t, NewStore(t.TempDir(), true))
	defer cleanup()

	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("ListTools() error = %v", err)
	}
	got := make([]string, 0, len(tools.Tools))
	for _, tool := range tools.Tools {
		got = append(got, tool.Name)
	}
	for _, want := range []string{"balda.memory.read", "balda.memory.remember"} {
		if !slices.Contains(got, want) {
			t.Fatalf("ListTools() = %v, want %s", got, want)
		}
	}
}

func newMemoryMCPTestSession(t *testing.T, store *Store) (context.Context, func(), *mcp.ClientSession) {
	t.Helper()

	server := mcp.NewServer(&mcp.Implementation{Name: "balda-memory-test", Version: "1.0.0"}, nil)
	RegisterTools(server, store)

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
