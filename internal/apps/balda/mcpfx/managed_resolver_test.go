package mcpfx

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/mcpbridge"
	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

type retainedOAuthFixture struct {
	revision  mcpcmd.Revision
	valuesErr error
}

func (f retainedOAuthFixture) GetMCPConnection(context.Context, string) (mcpcmd.Connection, bool, error) {
	return mcpcmd.Connection{ID: "worker", PublicID: "worker", Source: mcpcmd.SourceManaged}, true, nil
}
func (f retainedOAuthFixture) GetMCPRevision(context.Context, string, string) (mcpcmd.Revision, bool, error) {
	return f.revision, true, nil
}
func (f retainedOAuthFixture) ResolveValues(mcpcmd.Revision) (mcpcmd.LaunchValues, error) {
	return mcpcmd.LaunchValues{}, f.valuesErr
}

func TestManagedResolverProjectsUnboundOAuthWithoutUnsignedRequests(t *testing.T) {
	var requests atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { requests.Add(1); w.WriteHeader(http.StatusAccepted) }))
	defer upstream.Close()
	bridge := mcpbridge.New(nil, nil)
	if err := bridge.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = bridge.Close(context.Background()) })
	fixture := retainedOAuthFixture{revision: mcpcmd.Revision{ConnectionID: "worker", ID: "retained", Definition: mcpcmd.Definition{Transport: mcpcmd.TransportHTTP, URL: upstream.URL, OAuth: true}}}
	resolver := &ManagedResolver{Store: fixture, Values: fixture, Bridge: bridge}
	descriptor := runtimecatalogcmd.MCPServerDescriptor{ID: runtimecatalogcmd.ContributionID{Kind: runtimecatalogcmd.ContributionKindMCPServer, Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP, Name: "worker"}, Name: "worker"}, Name: "worker", ConfigRef: "worker", Revision: "retained", Transport: transportStreamableHTTP}
	config, err := resolver.ResolveLaunch(t.Context(), descriptor)
	if err != nil {
		t.Fatalf("valid unbound definition cannot construct a safe shell: %v", err)
	}
	if requests.Load() != 0 || config.URL == upstream.URL {
		t.Fatal("resolution contacted or exposed the unsigned upstream")
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, config.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range config.Headers {
		request.Header.Set(name, value)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || response.StatusCode != http.StatusUnauthorized || requests.Load() != 0 || string(body) != "MCP worker authorization unavailable\n" {
		t.Fatalf("unbound route did not deny safely: status=%d body=%q requests=%d err=%v", response.StatusCode, body, requests.Load(), err)
	}
	fixture.revision.Definition.AuthBinding = &mcpcmd.AuthBinding{ConnectionID: "another-worker", Resource: upstream.URL, Issuer: "https://issuer.example", ClientID: "client"}
	resolver.Store = fixture
	if _, err := resolver.ResolveLaunch(t.Context(), descriptor); !errors.Is(err, mcpcmd.ErrInvalid) {
		t.Fatalf("corrupt retained binding accepted: %v", err)
	}
	fixture.revision.Definition.AuthBinding = nil
	fixture.valuesErr = mcpcmd.ErrCredentials
	resolver.Store, resolver.Values = fixture, fixture
	if _, err := resolver.ResolveLaunch(t.Context(), descriptor); !errors.Is(err, mcpcmd.ErrCredentials) {
		t.Fatalf("missing protected values accepted: %v", err)
	}
}
