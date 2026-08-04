package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
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

func TestAdapterDeliveryFormatMapsRichAndPlain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		format     deliveryfmt.DeliveryFormat
		wantMrkdwn bool
	}{
		{name: "mrkdwn enables rich text", format: deliveryfmt.DeliveryFormatMrkdwn, wantMrkdwn: true},
		{name: "none disables rich text", format: deliveryfmt.DeliveryFormatNone, wantMrkdwn: false},
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
			if _, err := adapter.SendAgentReplyWithProviderMessageIDAndFormat(context.Background(), NewThreadLocator("T123", "C456", threadTS), tt.format, "hello"); err != nil {
				t.Fatalf("SendAgentReplyWithProviderMessageIDAndFormat() error = %v", err)
			}
			if got.Mrkdwn != tt.wantMrkdwn {
				t.Fatalf("mrkdwn = %v, want %v", got.Mrkdwn, tt.wantMrkdwn)
			}
		})
	}
}
