package sessionmemorymcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/sessionmemory"
)

func TestHeaderSessionResolverRequiresActiveBrokerBinding(t *testing.T) {
	t.Parallel()

	broker := NewContextBroker()
	if err := broker.SetBaseURL("http://127.0.0.1:12345/mcp/balda"); err != nil {
		t.Fatalf("SetBaseURL() error = %v", err)
	}
	locator, err := deliverycmd.NewLocator("telegram", "101:77", `{"chat_id":101,"topic_id":77}`, "tg-101-77")
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	current := CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{SessionID: locator.SessionID, AgentSessionID: "agent-101-77"},
	}
	binding, err := broker.Bind(current)
	if err != nil {
		t.Fatalf("Bind() error = %v", err)
	}

	var injected http.Header
	broker.Wrap(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		injected = r.Header.Clone()
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, binding.URL, nil))
	resolver := HeaderSessionResolver{Broker: broker}
	request := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: injected}}
	got, err := resolver.Resolve(context.Background(), request)
	if err != nil {
		t.Fatalf("Resolve(bound) error = %v", err)
	}
	if got.Locator.AddressKey != locator.AddressKey || got.Session.AgentSessionID != current.Session.AgentSessionID {
		t.Fatalf("Resolve(bound) = %+v, want %+v", got, current)
	}

	for _, test := range []struct {
		name   string
		header string
		value  string
	}{
		{name: "locator", header: HeaderSessionLocator, value: `{"channel_type":"telegram","address_key":"-100:77"}`},
		{name: "session", header: HeaderSessionID, value: "attacker-session"},
		{name: "agent session", header: HeaderAgentSessionID, value: "attacker-agent"},
		{name: "lineage", header: HeaderLineageID, value: "attacker-lineage"},
		{name: "binding", header: HeaderSessionBinding, value: "attacker-binding"},
	} {
		t.Run("rejects tampered "+test.name, func(t *testing.T) {
			tampered := injected.Clone()
			tampered.Set(test.header, test.value)
			tamperedRequest := &mcp.CallToolRequest{Extra: &mcp.RequestExtra{Header: tampered}}
			if _, err := resolver.Resolve(context.Background(), tamperedRequest); err == nil {
				t.Fatalf("Resolve(tampered %s) error = nil, want authentication failure", test.name)
			}
		})
	}
	if _, err := (HeaderSessionResolver{}).Resolve(context.Background(), request); err == nil {
		t.Fatal("Resolve(without broker) error = nil, want authentication failure")
	}
	_ = binding.Release()
}
