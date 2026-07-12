package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
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

func TestAdapterFormattingProfileMapsMarkdownAndPlain(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		profile    deliverycmd.Profile
		wantMrkdwn bool
	}{
		{name: "auto uses mrkdwn", profile: deliverycmd.Profile{Format: deliverycmd.FormatAuto}, wantMrkdwn: true},
		{name: "markdown uses mrkdwn", profile: deliverycmd.Profile{Format: deliverycmd.FormatMarkdown}, wantMrkdwn: true},
		{name: "plain disables mrkdwn", profile: deliverycmd.Profile{Format: deliverycmd.FormatPlain}, wantMrkdwn: false},
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
			if _, err := adapter.SendAgentReplyWithProviderMessageIDAndProfile(context.Background(), NewThreadLocator("T123", "C456", threadTS), tt.profile, "hello"); err != nil {
				t.Fatalf("SendAgentReplyWithProviderMessageIDAndProfile() error = %v", err)
			}
			if got.Mrkdwn != tt.wantMrkdwn {
				t.Fatalf("mrkdwn = %v, want %v", got.Mrkdwn, tt.wantMrkdwn)
			}
		})
	}
}

func TestAdapterRejectsHTMLFormatting(t *testing.T) {
	t.Parallel()

	adapter := NewAdapter(NewClientWithBaseURL("http://127.0.0.1", "xoxb-token"), zerolog.Nop())
	_, err := adapter.SendAgentReplyWithProviderMessageIDAndProfile(
		context.Background(),
		NewThreadLocator("T123", "C456", threadTS),
		deliverycmd.Profile{Format: deliverycmd.FormatHTML},
		"hello",
	)
	if err == nil {
		t.Fatal("SendAgentReplyWithProviderMessageIDAndProfile() error = nil, want unsupported html")
	}
}
