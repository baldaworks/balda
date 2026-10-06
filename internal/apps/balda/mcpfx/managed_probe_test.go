package mcpfx

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestOAuthProbeRequiresWorkerBindingBeforeAnyRequest(t *testing.T) {
	var requests atomic.Int32
	server := mcp.NewServer(&mcp.Implementation{Name: "anonymous-fixture", Version: "1"}, nil)
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); handler.ServeHTTP(w, r) }))
	defer upstream.Close()
	credentials, err := mcpmanage.New("")
	if err != nil {
		t.Fatal(err)
	}
	probe, err := NewManagedProbe(credentials, NewClientLauncher(), nil)
	if err != nil {
		t.Fatal(err)
	}
	_, err = probe.ProbeMCP(t.Context(), mcpcmd.Revision{ConnectionID: "worker", ID: "unbound", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL, OAuth: true}})
	if !errors.Is(err, mcpcmd.ErrAuthRequired) || requests.Load() != 0 {
		t.Fatal("OAuth probe authenticated anonymously before a trusted binding existed")
	}
}
