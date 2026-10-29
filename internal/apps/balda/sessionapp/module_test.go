package sessionapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	baldaagent "github.com/baldaworks/balda/internal/apps/balda/agent"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/balda/internal/apps/balda/sessionmemorymcp"
	"github.com/baldaworks/runtime/v2/agentproduct"
	adksession "google.golang.org/adk/v2/session"
)

type adapterSessionControl struct{}

func (*adapterSessionControl) Inspect(context.Context, adksession.Session) agentproduct.SessionControlSnapshot {
	return agentproduct.SessionControlSnapshot{}
}
func (*adapterSessionControl) ValidateModel(context.Context, adksession.Session, string) agentproduct.ModelValidation {
	return agentproduct.ModelValidation{}
}
func (*adapterSessionControl) ChangeModel(context.Context, adksession.Session, string) agentproduct.ModelChange {
	return agentproduct.ModelChange{}
}
func (*adapterSessionControl) FastForward(context.Context, adksession.Session, agentproduct.ContextPackage) agentproduct.ContextChange {
	return agentproduct.ContextChange{}
}

func TestRuntimeAdaptersPreserveSessionControl(t *testing.T) {
	t.Parallel()

	control := &adapterSessionControl{}
	agentSide := &baldaagent.BuiltRuntime{
		SessionControl: control, ProviderBindingJSON: `{"root_provider_id":"provider"}`,
		RecipeModel: "model-bound", ConfirmedModel: "model-live",
	}
	sessionSide := sessionRuntime(agentSide)
	if sessionSide.SessionControl != control || sessionSide.ProviderBindingJSON != agentSide.ProviderBindingJSON ||
		sessionSide.RecipeModel != agentSide.RecipeModel || sessionSide.ConfirmedModel != agentSide.ConfirmedModel {
		t.Fatal("agent-to-session adapter lost runtime product metadata")
	}
	roundTrip := agentRuntime(sessionSide)
	if roundTrip.SessionControl != control || roundTrip.ProviderBindingJSON != agentSide.ProviderBindingJSON ||
		roundTrip.RecipeModel != agentSide.RecipeModel || roundTrip.ConfirmedModel != agentSide.ConfirmedModel {
		t.Fatal("session-to-agent adapter lost runtime product metadata")
	}
}

func TestSessionMCPBinderAdapterEnabledForSessionCapabilities(t *testing.T) {
	t.Parallel()

	withSkills := baldaagent.SessionCapabilityBinding{Skills: baldaagent.SkillMetadataProjection{
		Snapshot: "snapshot-prism",
		Skills: []baldaagent.SkillPromptMetadata{{
			Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindPlugin, Name: "prism"},
			Name:   "story",
		}},
	}}
	for _, test := range []struct {
		name         string
		memory       bool
		capabilities baldaagent.SessionCapabilityBinding
		want         bool
	}{
		{name: "session memory", memory: true, want: true},
		{name: "advertised skill", capabilities: withSkills, want: true},
		{name: "no scoped tools", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			adapter := SessionMCPBinderAdapter{Enabled: test.memory}
			if got := adapter.SessionMCPEnabled(test.capabilities); got != test.want {
				t.Fatalf("SessionMCPEnabled() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestSessionMCPBinderAdapterBindsResolvedRuntimeSnapshot(t *testing.T) {
	t.Parallel()

	broker := sessionmemorymcp.NewContextBroker()
	if err := broker.SetBaseURL("http://127.0.0.1:12345/mcp/balda"); err != nil {
		t.Fatalf("SetBaseURL() error = %v", err)
	}
	locator, err := deliverycmd.NewLocator("telegram", "101:77", `{"chat_id":101,"topic_id":77}`, "tg-101-77")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	adapter := SessionMCPBinderAdapter{Broker: broker}
	binding, err := adapter.BindSession(context.Background(), baldaagent.SessionRuntimeRequest{
		Locator:           locator,
		AgentSessionID:    "agent-101-77",
		RuntimeSnapshotID: "snapshot-prism",
	})
	if err != nil {
		t.Fatalf("BindSession() error = %v", err)
	}
	t.Cleanup(func() { _ = binding.Release() })

	var gotSnapshot string
	broker.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		gotSnapshot = request.Header.Get(sessionmemorymcp.HeaderRuntimeSnapshotID)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, binding.Config.URL, nil))
	if gotSnapshot != "snapshot-prism" {
		t.Fatalf("bound runtime snapshot = %q, want snapshot-prism", gotSnapshot)
	}
}
