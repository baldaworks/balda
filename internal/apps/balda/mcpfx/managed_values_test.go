package mcpfx

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/mcpmanage"
	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestManagedSSECredentialsNeverReachForeignEndpointEvent(t *testing.T) {
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Worker-Secret") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "event: endpoint\ndata: %s\n\n", foreign.URL)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer origin.Close()
	s, err := mcpmanage.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "sse-event",
		Definition: mcpcmd.Definition{Transport: mcpcmd.TransportSSE, URL: origin.URL}}, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
		"Authorization":   {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "sse-protected-fixture"},
		"X-Worker-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: "sse-custom-protected-fixture"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	config, err := ResolveManagedLaunch(r, s)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	instance, err := NewClientLauncher().Start(ctx, mcpruntime.InstanceKey{Name: "worker"}, config)
	if instance != nil {
		_ = instance.Close(ctx)
	}
	if err == nil || leaked.Load() {
		t.Fatal("protected credentials reached a foreign SSE endpoint")
	}
}

func TestManagedProjectionFailsClosedWhenProviderCannotEnforceCredentialOrigin(t *testing.T) {
	registry := mcpregistry.New(nil)
	projector, err := NewRegistryProjector(registry)
	if err != nil {
		t.Fatal(err)
	}
	key := mcpruntime.InstanceKey{Name: "worker"}
	config := mcpruntime.LaunchConfig{Transport: transportStreamableHTTP, URL: "https://worker.example/mcp",
		Headers: map[string]string{"Authorization": "protected-runtime-fixture"}, EnforceHTTPOrigin: true}
	outcome, err := projector.Project(t.Context(), key, config)
	_, published := registry.Get(RegistryID(key))
	if err == nil || outcome != runtimecatalogcmd.MCPProjectionUnsupported || published {
		t.Fatal("provider projection silently discarded the credential boundary")
	}
}

func TestManagedMCPCredentialsNeverReachCrossOriginRedirect(t *testing.T) {
	const secret = "redirect-protected-credential-fixture"
	t.Setenv("MCP_REDIRECT_ENV_FIXTURE", "redirect-environment-credential-fixture")
	var leaked atomic.Bool
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("X-Worker-Secret") != "" || r.Header.Get("X-Deployment-Secret") != "" {
			leaked.Store(true)
		}
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer foreign.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, foreign.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	s, err := mcpmanage.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
	if err != nil {
		t.Fatal(err)
	}
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		r, err := s.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "redirect-revision",
			Definition: mcpcmd.Definition{Transport: transport, URL: origin.URL}}, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
			"Authorization":       {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: secret},
			"X-Worker-Secret":     {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: secret},
			"X-Deployment-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: "MCP_REDIRECT_ENV_FIXTURE"},
		}})
		if err != nil {
			t.Fatal(err)
		}
		config, err := ResolveManagedLaunch(r, s)
		if err != nil {
			t.Fatal(err)
		}
		instance, err := NewClientLauncher().Start(t.Context(), mcpruntime.InstanceKey{Name: "worker"}, config)
		if instance != nil {
			_ = instance.Close(t.Context())
		}
		if err == nil || leaked.Load() {
			t.Fatalf("%s redirect sent protected credentials to another origin", transport)
		}
	}
}

func TestManagedMCPReceivesProtectedAndEnvironmentHeaders(t *testing.T) {
	const secret = "worker-literal-fixture"
	t.Setenv("MCP_WORKER_ENV_FIXTURE", "worker-environment-fixture")
	for _, transport := range []mcpcmd.Transport{mcpcmd.TransportHTTP, mcpcmd.TransportSSE} {
		t.Run(string(transport), func(t *testing.T) {
			var authorized atomic.Int32
			server := mcp.NewServer(&mcp.Implementation{Name: "credential-fixture", Version: "1"}, nil)
			var handler http.Handler
			if transport == mcpcmd.TransportSSE {
				handler = mcp.NewSSEHandler(func(*http.Request) *mcp.Server { return server }, nil)
			} else {
				handler = mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)
			}
			httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/redirect" {
					http.Redirect(w, r, "/mcp", http.StatusTemporaryRedirect)
					return
				}
				if r.Header.Get("X-Worker-Secret") != secret || r.Header.Get("X-Deployment-Secret") != "worker-environment-fixture" {
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				authorized.Add(1)
				handler.ServeHTTP(w, r)
			}))
			defer httpServer.Close()
			s, err := mcpmanage.New(base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 32)))
			if err != nil {
				t.Fatal(err)
			}
			revision, err := s.PrepareRevision(nil, mcpcmd.Revision{ConnectionID: "worker", ID: "revision-1",
				Definition: mcpcmd.Definition{Transport: transport, URL: httpServer.URL + "/redirect"}}, mcpcmd.ValueEdits{Headers: map[string]mcpcmd.ValueEdit{
				"X-Worker-Secret":     {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueProtected, Value: secret},
				"X-Deployment-Secret": {Operation: mcpcmd.ValueSet, Kind: mcpcmd.ValueEnvironment, Value: "MCP_WORKER_ENV_FIXTURE"},
			}})
			if err != nil {
				t.Fatal(err)
			}
			config, err := ResolveManagedLaunch(revision, s)
			if err != nil {
				t.Fatal(err)
			}
			instance, err := NewClientLauncher().Start(t.Context(), mcpruntime.InstanceKey{Name: "worker"}, config)
			if err != nil {
				t.Fatal(err)
			}
			if err := instance.Close(t.Context()); err != nil {
				t.Fatal(err)
			}
			if authorized.Load() == 0 {
				t.Fatal("managed MCP was not contacted with resolved credentials")
			}
		})
	}
}
