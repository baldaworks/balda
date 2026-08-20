package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryfmt"
	"github.com/rs/zerolog"
)

const threadTS = "1712345678.000100"

func TestAdapterSendsThreadReply(t *testing.T) {
	t.Parallel()

	var got postMessageRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		_ = json.NewEncoder(w).Encode(postMessageResponse{OK: true, TS: "1712345678.000200"})
	}))
	t.Cleanup(server.Close)

	adapter := NewAdapter(NewClientWithBaseURL(server.URL, "xoxb-token"), zerolog.Nop())
	providerID, err := adapter.SendAgentReplyWithProviderMessageID(
		context.Background(),
		NewThreadLocator("T123", "C456", threadTS),
		"hello",
	)
	if err != nil {
		t.Fatalf("SendAgentReplyWithProviderMessageID() error = %v", err)
	}
	if providerID != "1712345678.000200" {
		t.Fatalf("providerID = %q", providerID)
	}
	if got.Channel != "C456" || got.ThreadTS != threadTS || got.Text != "hello" {
		t.Fatalf("request = %+v", got)
	}
}

func TestAdapterDeliversTypedRichAndPlainMessages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		message    deliveryfmt.Message
		wantMrkdwn bool
	}{
		{name: "mrkdwn enables rich text", message: deliveryfmt.Message{Name: deliveryfmt.NameSlackMrkdwn, Text: "hello"}, wantMrkdwn: true},
		{name: "plain disables rich text", message: deliveryfmt.Message{Name: deliveryfmt.NamePlainText, Text: "hello"}, wantMrkdwn: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var got postMessageRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if err := json.NewDecoder(r.Body).Decode(&got); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				_ = json.NewEncoder(w).Encode(postMessageResponse{OK: true, TS: "1712345678.000200"})
			}))
			t.Cleanup(server.Close)

			adapter := NewAdapter(NewClientWithBaseURL(server.URL, "xoxb-token"), zerolog.Nop())
			if _, err := adapter.Deliver(context.Background(), NewThreadLocator("T123", "C456", threadTS), deliverycmd.Operation{Kind: deliverycmd.OperationAgentReply, Message: &tt.message}); err != nil {
				t.Fatalf("Deliver() error = %v", err)
			}
			if got.Mrkdwn != tt.wantMrkdwn {
				t.Fatalf("mrkdwn = %v, want %v", got.Mrkdwn, tt.wantMrkdwn)
			}
		})
	}
}
