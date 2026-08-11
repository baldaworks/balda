package sessionmemorymcp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/sessionmemory"
)

func TestContextBrokerBindsConcurrentSessionsAndOverwritesCallerHeaders(t *testing.T) {
	t.Parallel()

	broker := NewContextBroker()
	if err := broker.SetBaseURL("http://127.0.0.1:12345/mcp/balda"); err != nil {
		t.Fatalf("SetBaseURL() error = %v", err)
	}
	personal := brokerTestSession(t, "telegram:101:77", "tg-101-77", "agent-personal")
	group := brokerTestSession(t, "telegram:-100:77", "tg--100-77", "agent-group")
	personalBinding, err := broker.Bind(personal)
	if err != nil {
		t.Fatalf("Bind(personal) error = %v", err)
	}
	groupBinding, err := broker.Bind(group)
	if err != nil {
		t.Fatalf("Bind(group) error = %v", err)
	}
	if personalBinding.ID == groupBinding.ID || personalBinding.URL == groupBinding.URL {
		t.Fatal("concurrent session bindings are not distinct")
	}
	if strings.Contains(personalBinding.URL, personal.Locator.AddressKey) || strings.Contains(personalBinding.URL, personal.Locator.SessionID) {
		t.Fatalf("binding URL leaked session identity: %q", personalBinding.URL)
	}

	handler := broker.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload := map[string]string{
			"locator": r.Header.Get(HeaderSessionLocator),
			"session": r.Header.Get(HeaderSessionID),
			"agent":   r.Header.Get(HeaderAgentSessionID),
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(payload)
	}))

	var wg sync.WaitGroup
	for _, test := range []struct {
		binding ContextBinding
		want    CurrentSession
	}{
		{binding: personalBinding, want: personal},
		{binding: groupBinding, want: group},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, test.binding.URL, nil)
			req.Header.Set(HeaderSessionLocator, `{"channel_type":"telegram","address_key":"attacker"}`)
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusOK {
				t.Errorf("bound request status = %d, want 200", res.Code)
				return
			}
			var got map[string]string
			if err := json.NewDecoder(res.Body).Decode(&got); err != nil {
				t.Errorf("decode bound request: %v", err)
				return
			}
			var wantLocator deliverycmd.Locator
			if err := json.Unmarshal([]byte(got["locator"]), &wantLocator); err != nil {
				t.Errorf("decode injected locator: %v", err)
				return
			}
			if wantLocator.AddressKey != test.want.Locator.AddressKey || got["session"] != test.want.Session.SessionID || got["agent"] != test.want.Session.AgentSessionID {
				t.Errorf("injected context = %#v/%#v, want locator %q session %q agent %q", wantLocator, got, test.want.Locator.AddressKey, test.want.Session.SessionID, test.want.Session.AgentSessionID)
			}
		}()
	}
	wg.Wait()

	if err := personalBinding.Release(); err != nil {
		t.Fatalf("Release(personal) error = %v", err)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, personalBinding.URL, nil))
	if res.Code != http.StatusUnauthorized {
		t.Fatalf("released binding status = %d, want 401", res.Code)
	}
	_ = groupBinding.Release()
}

func brokerTestSession(t *testing.T, addressKey, sessionID, agentSessionID string) CurrentSession {
	t.Helper()
	channelType, _, _ := strings.Cut(addressKey, ":")
	locator, err := deliverycmd.NewLocator(channelType, addressKey, `{"chat_id":1,"topic_id":77}`, sessionID)
	if err != nil {
		t.Fatalf("NewLocator() error = %v", err)
	}
	return CurrentSession{
		Locator: locator,
		Session: sessionmemory.SessionRef{SessionID: sessionID, AgentSessionID: agentSessionID},
	}
}
