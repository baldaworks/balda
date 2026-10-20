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
)

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
