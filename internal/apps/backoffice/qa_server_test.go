package backoffice

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestServeQAWithoutApplicationState(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ready := make(chan string, 1)
	finished := make(chan error, 1)
	go func() {
		finished <- ServeQA(ctx, "127.0.0.1:0", func(address string) { ready <- address })
	}()
	var address string
	select {
	case address = <-ready:
	case err := <-finished:
		t.Fatalf("QA server stopped before listening: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("QA server did not start")
	}
	client := &http.Client{Timeout: 3 * time.Second}
	preview, err := client.Get("http://" + address + "/qa/ui/account")
	if err != nil {
		t.Fatal(err)
	}
	previewHTML, err := io.ReadAll(preview.Body)
	_ = preview.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	assetPath := appAssetPath(t, string(previewHTML), "css")
	for path, want := range map[string]int{
		"/qa/ui/":        http.StatusOK,
		"/qa/ui/account": http.StatusOK,
		assetPath:        http.StatusOK,
		"/account":       http.StatusNotFound,
	} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_ = response.Body.Close()
		if response.StatusCode != want {
			t.Errorf("GET %s = %d, want %d", path, response.StatusCode, want)
		}
	}
	cancel()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatalf("QA server shutdown: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("QA server did not stop")
	}
}

func TestServeQARejectsNonLoopbackAndOccupiedListener(t *testing.T) {
	t.Parallel()
	if err := ServeQA(t.Context(), "0.0.0.0:0", nil); err == nil || !strings.Contains(err.Error(), "loopback") {
		t.Fatalf("non-loopback address error = %v", err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := ServeQA(t.Context(), listener.Addr().String(), nil); err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("occupied listener error = %v", err)
	}
}

func TestQAOperatorDestinations(t *testing.T) {
	t.Parallel()
	handler, err := QAHandler("/review")
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ path, text string }{
		{"access/users/primary-demo", "Primary"},
		{"aliases/alerts", "slackagent:T1:C1"},
		{"hooks/session-note", "session-note"},
		{"mcp/oauth/device/qa-attempt", "Authorization status"},
		{"memory/2", "Review staging first"},
		{"plugins/installed/installed-demo", "installed-demo"},
		{"plugins/marketplaces/definitions/team", "team"},
		{"providers/configured-pool", "configured-pool"},
		{"providers/managed-codex", "managed-codex"},
		{"schedules/config:morning", "config:morning"},
		{"schedules/config:morning?run_id=qa-run-failed", "Report delivery failed."},
		{"schedules/config:morning?run_id=qa-run-succeeded", "Synthetic summary output"},
		{"mcp-authorizing", "Cancel authorization"},
		{"webauthn-register", "Register your passkey"},
		{"mcp-saved-start-failed", `href="/review/qa/ui/mcp/connections/qa-worker"`},
		{"overview", `href="/review/qa/ui/access-list"`},
	} {
		t.Run(test.path, func(t *testing.T) {
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, httptest.NewRequest(http.MethodGet, "/review/qa/ui/"+test.path, nil))
			want := http.StatusOK
			if test.path == "mcp-saved-start-failed" {
				want = http.StatusServiceUnavailable
			}
			if res.Code != want || !strings.Contains(res.Body.String(), test.text) {
				t.Errorf("status %d, want %d and content %q", res.Code, want, test.text)
			}
		})
	}
}
