package catalogapp

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionapp"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorymcp"
	"github.com/baldaworks/balda/internal/apps/balda/skillmcp"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/runtime/v2/mcpregistry"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSkillLoaderExecutesThroughHostedACPAndPoolProviders(t *testing.T) {
	stateDir := t.TempDir()
	skillRoot := t.TempDir()
	t.Cleanup(func() {
		makeWritable(stateDir)
		makeWritable(skillRoot)
	})
	writeFile(t, filepath.Join(skillRoot, "provider-skill", "SKILL.md"), "---\nname: provider-skill\ndescription: Provider execution fixture.\n---\nloaded by provider\n")
	provider, err := baldastate.NewSQLiteProvider(t.Context(), filepath.Join(stateDir, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	registry := mcpregistry.New(nil)
	catalog, err := NewRuntime(stateDir, skillRoot, "", provider, nil, nil, registry, commandcmd.NewRegistry(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := catalog.PreparePluginCandidate(t.Context(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := catalog.PublishCandidate(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	snapshot := candidate.Snapshot
	skills, err := baldaagent.NewSkillManager(catalog, catalog, baldaagent.SkillMetadataBudget{})
	if err != nil {
		t.Fatal(err)
	}
	broker := sessionmemorymcp.NewContextBroker()
	server := mcp.NewServer(&mcp.Implementation{Name: "provider-skill-loader", Version: "1"}, nil)
	skillmcp.RegisterTools(server, skills, sessionmemorymcp.HeaderSessionResolver{Broker: broker})
	httpServer := httptest.NewServer(broker.Wrap(mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return server }, nil)))
	t.Cleanup(func() {
		httpServer.CloseClientConnections()
		httpServer.Close()
	})
	if err := broker.SetBaseURL(httpServer.URL); err != nil {
		t.Fatal(err)
	}
	option := providerExecutionOptions{
		sessionMCPBinder: sessionapp.SessionMCPBinderAdapter{Broker: broker},
		skillLoader:      true,
		bundledURL:       httpServer.URL,
	}

	// The provider must read the retained revision, not the now-changed source.
	writeFile(t, filepath.Join(skillRoot, "provider-skill", "SKILL.md"), "---\nname: provider-skill\ndescription: Changed source.\n---\nchanged after pin\n")

	t.Run("hosted and pool", func(t *testing.T) {
		testHostedPoolInvocationWithTools(t, catalog, registry, snapshot.ID, nil, nil, []string{"balda.skill.load"}, nil, false, option)
	})
	t.Run("ACP and pool", func(t *testing.T) {
		for _, name := range []string{"fresh", "replacement"} {
			t.Run(name, func(t *testing.T) {
				// Each helper call stops its managers. The next call creates new
				// runtimes, ADK sessions and broker bindings at the same locator.
				testACPInvocationWithServers(t, catalog, registry, snapshot.ID, nil, nil, nil, false, false, option)
			})
		}
	})
}
