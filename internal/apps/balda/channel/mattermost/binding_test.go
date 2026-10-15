package mattermost

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestMattermostInvitationExcludedFromFetchedThread(t *testing.T) {
	payload := "bind_" + strings.Repeat("a", 32)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"posts":{"credential":{"id":"credential","user_id":"user-1","message":"`+payload+`"},"damaged":{"id":"damaged","message":"`+payload[:len(payload)-3]+`"},"ordinary":{"id":"ordinary","user_id":"bot-1","message":"Useful discussion"}}}`)
	}))
	defer api.Close()
	thread, err := NewClient(api.URL, "synthetic-token", "bot-1").GetPostThread(t.Context(), "credential")
	if err != nil || len(thread.Posts) != 1 || thread.Posts["ordinary"].Message != "Useful discussion" || !threadContainsBot(thread, "bot-1") {
		t.Fatalf("safe history %+v: %v", thread, err)
	}
}

func TestMattermostCredentialCommandHasSafeHTTPOutcome(t *testing.T) {
	payload := "bind_" + strings.Repeat("a", 32)
	server := newTestCommandServer(&commandRecorder{})
	request, response := commandRequest(url.Values{"token": {testCommandToken}, "command": {"/balda"}, "text": {payload}, "channel_id": {testChannelID}, "user_id": {"account-1"}})
	server.handleCommand(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), payload) {
		t.Fatalf("unsafe command outcome %d %s", response.Code, response.Body.String())
	}
}
