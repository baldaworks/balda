package slackagent

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSlackBindingIdentityRequiresConfiguredBot(t *testing.T) {
	for _, response := range []string{
		`{"ok":false,"error":"invalid_auth"}`,
		`{"ok":true,"team_id":"T123","user_id":"U123"}`,
		`{"ok":true,"bot_id":"B123","user_id":"UBOT"}`,
	} {
		t.Run(response, func(t *testing.T) {
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, response) }))
			defer api.Close()
			if _, err := NewClientWithBaseURL(api.URL, "synthetic-token").BindingIdentity(t.Context()); err == nil {
				t.Fatal("unverified or non-bot identity accepted")
			}
		})
	}
}

func TestSlackInvitationHistoryExclusion(t *testing.T) {
	payload := "bind_" + strings.Repeat("a", 32)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"ok":true,"messages":[{"ts":"1.1","user":"U123","text":"`+payload+`"},{"ts":"1.2","user":"U123","text":"please use `+payload[:len(payload)-3]+`"},{"ts":"1.3","user":"U123","text":"Useful design discussion"}]}`)
	}))
	defer api.Close()
	snapshot, err := NewClientWithBaseURL(api.URL, "synthetic-token").ReadThreadBefore(t.Context(), "C123", "1.1", "2.0")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Messages) != 1 || snapshot.Messages[0].Text != "Useful design discussion" {
		t.Fatalf("history %+v", snapshot.Messages)
	}
	// Custom history readers must receive the same filtering at formatting/hydration.
	snapshot.Messages = append(snapshot.Messages, ThreadMessage{TS: "1.4", Text: payload})
	prompt, err := FormatThreadContext(snapshot, "Continue the design")
	if err != nil || strings.Contains(prompt, payload) || !strings.Contains(prompt, "Useful design discussion") {
		t.Fatalf("unsafe context %q: %v", prompt, err)
	}
	// Bypassing HTTP admission still cannot send a credential to a model/queue.
	if _, err := NewInboundProcessor(nil, nil, nil, nil, nil).ProcessInbound(context.Background(), IngressEnvelope{Chat: NormalizeChatRequest(NewThreadLocator("T123", "D123", "1.1"), Event{Text: payload}, time.Now())}); err != nil {
		t.Fatal(err)
	}
}
