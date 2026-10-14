package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
)

func TestExhaustedInboundRetriesReportFailureInOriginalThread(t *testing.T) {
	var posts []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v4/posts" {
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
			return
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		posts = append(posts, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"failure-post"}`))
	}))
	defer server.Close()
	processor := &recordingProcessor{inboundResult: turncmd.InboundSettlement{Outcome: turncmd.InboundRetry}, inboundErr: errors.New("private-diagnostic-credential")}
	ingress := newTestIngress(processor, "bot-1", "balda")
	ingress.client = NewClient(server.URL, "test-token", "bot-1")
	msg := InboundMessage{Locator: NewDMLocator("original-channel", "user-1"), PostID: "original-post", RootID: "original-thread"}
	ingress.processInboundWithRetry(context.Background(), msg.PostID, msg)
	if len(posts) != 1 || posts[0]["channel_id"] != "original-channel" || posts[0]["root_id"] != "original-thread" || posts[0]["pending_post_id"] == "" {
		t.Fatalf("failure routed incorrectly: %+v", posts)
	}
	if strings.Contains(posts[0]["message"], "private-diagnostic-credential") {
		t.Fatal("private diagnostic leaked into chat")
	}
}
