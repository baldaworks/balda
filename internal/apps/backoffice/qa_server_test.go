package backoffice

import (
	"context"
	"net"
	"net/http"
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
	for path, want := range map[string]int{
		"/qa/ui/":         http.StatusOK,
		"/qa/ui/account":  http.StatusOK,
		"/assets/app.css": http.StatusOK,
		"/account":        http.StatusNotFound,
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
