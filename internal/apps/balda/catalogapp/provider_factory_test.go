package catalogapp

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/normahq/runtime/v2/agentconfig"
	"github.com/normahq/runtime/v2/agentfactory"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestProviderFactoryConstructsCompleteUnavailableMCPShells(t *testing.T) {
	var requests atomic.Int32
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer remote.Close()
	configs := map[string]agentconfig.Config{
		"hosted": {Type: agentconfig.AgentTypeOpenAI, OpenAI: &agentconfig.LocalAPIConfig{APIKey: "fixture", Model: "fixture"}},
		"acp":    {Type: agentconfig.AgentTypeGenericACP, GenericACP: &agentconfig.ACPConfig{Cmd: []string{"/does/not/exist"}}},
	}
	registry := mcpregistry.New(map[string]agentconfig.MCPServerConfig{
		"revision-one": {Type: agentconfig.MCPServerTypeHTTP, URL: remote.URL + "/one", Headers: map[string]string{"X-Revision": "one"}},
		"revision-two": {Type: agentconfig.MCPServerTypeSSE, URL: remote.URL + "/two"},
	})
	factory := NewProviderFactory(configs, registry)
	for _, id := range []string{"hosted", "acp"} {
		t.Run(id, func(t *testing.T) {
			built, err := factory.BuildScoped(t.Context(), agentfactory.BuildRequest{AgentID: id, WorkingDirectory: t.TempDir()}, map[string][]string{id: {"revision-one", "revision-two"}})
			if err != nil {
				t.Fatalf("construct complete unavailable shell: %v", err)
			}
			if err := built.(io.Closer).Close(); err != nil {
				t.Fatal(err)
			}
			if requests.Load() != 0 {
				t.Fatal("provider construction contacted a selected remote")
			}
			if _, err := factory.BuildScoped(t.Context(), agentfactory.BuildRequest{AgentID: id, WorkingDirectory: t.TempDir()}, map[string][]string{id: {"revision-one", "missing-revision"}}); err == nil {
				t.Fatal("missing exact selected config accepted")
			}
		})
	}
}
