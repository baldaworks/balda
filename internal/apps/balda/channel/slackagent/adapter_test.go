package slackagent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	baldaslack "github.com/normahq/balda/internal/apps/balda/channel/slack"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/rs/zerolog"
)

func TestAdapterDeliverAgentReplyReturnsProviderMessageID(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("path = %q, want /chat.postMessage", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"ts": "1712345678.000100",
		})
	}))
	t.Cleanup(server.Close)

	client := baldaslack.NewClientWithBaseURL(server.URL, "xoxb-token")
	adapter := NewAdapter(client, zerolog.Nop(), AdapterConfig{})
	locator := NewThreadLocator("T123", "C456", "thread-789")

	result, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{
		Kind: deliverycmd.OperationAgentReply,
		Text: "hello",
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	if got, want := result.ProviderMessageID, "1712345678.000100"; got != want {
		t.Fatalf("provider_message_id = %q, want %q", got, want)
	}
}

func TestAdapterDeliverAgentReplyAppendsSuggestedPromptsWhenEnabled(t *testing.T) {
	t.Parallel()

	var got map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat.postMessage" {
			t.Fatalf("path = %q, want /chat.postMessage", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true,
			"ts": "1712345678.000100",
		})
	}))
	t.Cleanup(server.Close)

	client := baldaslack.NewClientWithBaseURL(server.URL, "xoxb-token")
	adapter := NewAdapter(client, zerolog.Nop(), AdapterConfig{SuggestedPrompts: true})
	locator := NewConversationLocator("T123", "C456")

	_, err := adapter.Deliver(t.Context(), locator, deliverycmd.Operation{
		Kind: deliverycmd.OperationAgentReply,
		Text: "hello",
	})
	if err != nil {
		t.Fatalf("Deliver() error = %v", err)
	}
	text, _ := got["text"].(string)
	if text == "hello" || text == "" {
		t.Fatalf("request text = %q, want appended suggested prompts", text)
	}
}
