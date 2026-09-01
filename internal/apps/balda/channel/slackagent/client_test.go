package slackagent

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestClientCurrentAgentMethodsUseExactJSONContracts(t *testing.T) {
	t.Parallel()
	type observedRequest struct {
		path string
		body map[string]any
	}
	requests := make(chan observedRequest, 6)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer xoxb-test-token" {
			t.Errorf("Authorization = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		requests <- observedRequest{path: r.URL.Path, body: body}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/chat.postMessage" || r.URL.Path == "/chat.startStream" {
			_, _ = w.Write([]byte(`{"ok":true,"ts":"1782234987.693923"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-test-token")
	ctx := context.Background()

	if _, err := client.PostMessage(ctx, "D123", "1782234671.392669", "final", true); err != nil {
		t.Fatalf("PostMessage() error = %v", err)
	}
	if err := client.SetSessionStatus(ctx, SetSessionStatusRequest{
		ChannelID:       "D123",
		ThreadTS:        "1782234671.392669",
		Status:          SessionStatusProcessing,
		Title:           "Research",
		InitiatorUserID: "U123",
	}); err != nil {
		t.Fatalf("SetSessionStatus() error = %v", err)
	}
	if err := client.RenameSession(ctx, "D123", "1782234671.392669", "Renamed"); err != nil {
		t.Fatalf("RenameSession() error = %v", err)
	}
	if _, err := client.StartStream(ctx, "D123", "1782234671.392669", "first"); err != nil {
		t.Fatalf("StartStream() error = %v", err)
	}
	if err := client.AppendStream(ctx, "D123", "1782234987.693923", " next"); err != nil {
		t.Fatalf("AppendStream() error = %v", err)
	}
	if err := client.StopStream(ctx, "D123", "1782234987.693923", "done", SessionStatusActive); err != nil {
		t.Fatalf("StopStream() error = %v", err)
	}

	want := []observedRequest{
		{path: "/chat.postMessage", body: map[string]any{"channel": "D123", "thread_ts": "1782234671.392669", "text": "final", "mrkdwn": true}},
		{path: "/agents.sessions.setStatus", body: map[string]any{"channel_id": "D123", "thread_ts": "1782234671.392669", "status": "processing", "title": "Research", "initiator_user_id": "U123"}},
		{path: "/agents.sessions.rename", body: map[string]any{"channel_id": "D123", "thread_ts": "1782234671.392669", "title": "Renamed"}},
		{path: "/chat.startStream", body: map[string]any{"channel": "D123", "thread_ts": "1782234671.392669", "markdown_text": "first"}},
		{path: "/chat.appendStream", body: map[string]any{"channel": "D123", "ts": "1782234987.693923", "markdown_text": " next"}},
		{path: "/chat.stopStream", body: map[string]any{"channel": "D123", "ts": "1782234987.693923", "markdown_text": "done", "session_status": "active"}},
	}
	for i := range want {
		got := <-requests
		if !reflect.DeepEqual(got, want[i]) {
			t.Errorf("request %d = %+v, want %+v", i, got, want[i])
		}
	}
}

func TestClientValidatesSessionStatusAndRuneSafeTitleLimit(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-test-token")
	if err := client.SetSessionStatus(context.Background(), SetSessionStatusRequest{
		ChannelID: "D123", ThreadTS: "1.2", Status: SessionStatus("waiting"),
	}); err == nil || !strings.Contains(err.Error(), "invalid") {
		t.Fatalf("SetSessionStatus() error = %v, want invalid status", err)
	}
	validUnicodeTitle := strings.Repeat("界", maxSessionTitleRunes)
	if err := client.RenameSession(context.Background(), "D123", "1.2", validUnicodeTitle); err != nil {
		t.Fatalf("RenameSession() valid 200-rune title error = %v", err)
	}
	tooLong := validUnicodeTitle + "界"
	if err := client.RenameSession(context.Background(), "D123", "1.2", tooLong); err == nil || !strings.Contains(err.Error(), "1-200") {
		t.Fatalf("RenameSession() error = %v, want title limit", err)
	}
}

func TestClientClassifiesSlackFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		status         int
		body           string
		retryAfter     string
		wantCode       string
		wantRetryable  bool
		wantRetryAfter time.Duration
	}{
		{name: "rate limit", status: http.StatusTooManyRequests, body: `slow down`, retryAfter: "7", wantRetryable: true, wantRetryAfter: 7 * time.Second},
		{name: "server error", status: http.StatusBadGateway, body: `upstream unavailable`, wantRetryable: true},
		{name: "malformed json", status: http.StatusOK, body: `{`, wantCode: "malformed_response", wantRetryable: true},
		{name: "transient slack code", status: http.StatusOK, body: `{"ok":false,"error":"internal_error"}`, wantCode: "internal_error", wantRetryable: true},
		{name: "permanent slack code", status: http.StatusOK, body: `{"ok":false,"error":"invalid_auth"}`, wantCode: "invalid_auth"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if test.retryAfter != "" {
					w.Header().Set("Retry-After", test.retryAfter)
				}
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			t.Cleanup(server.Close)
			client := NewClientWithBaseURL(server.URL, "xoxb-secret-value")
			err := client.SetSessionStatus(context.Background(), SetSessionStatusRequest{
				ChannelID: "D123", ThreadTS: "1.2", Status: SessionStatusProcessing,
			})
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %T %v, want APIError", err, err)
			}
			if apiErr.Code != test.wantCode || apiErr.Retryable != test.wantRetryable || apiErr.RetryAfter != test.wantRetryAfter {
				t.Fatalf("APIError = %+v", apiErr)
			}
			if IsRetryableSlackError(err) != test.wantRetryable {
				t.Fatalf("IsRetryableSlackError() = %v", IsRetryableSlackError(err))
			}
			if strings.Contains(err.Error(), "xoxb-secret-value") {
				t.Fatalf("error leaked token: %v", err)
			}
		})
	}
}

func TestClientRejectsMissingStartStreamTimestamp(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(server.Close)
	client := NewClientWithBaseURL(server.URL, "xoxb-test-token")
	_, err := client.StartStream(context.Background(), "D123", "1.2", "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "malformed_response" {
		t.Fatalf("StartStream() error = %T %v", err, err)
	}
}
