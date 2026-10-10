package httpfx

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestServerBindsAfterStartAndStopsGracefully(t *testing.T) {
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ready")
	}))
	if server.Addr() != nil {
		t.Fatal("server bound before Start")
	}
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + server.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "ready" {
		t.Errorf("response body = %q, want ready", body)
	}
	if err := server.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	default:
		t.Fatal("Done not closed after Stop")
	}
	if err := server.Err(); err != nil {
		t.Errorf("Err after graceful Stop = %v, want nil", err)
	}
	if err := server.Start(); err == nil {
		t.Fatal("Start reported success without rebinding after Stop")
	}
}

func TestServerReportsBusyBind(t *testing.T) {
	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()
	server := NewServer(occupied.Addr().String(), http.NotFoundHandler())
	err = server.Start()
	if err == nil || !strings.Contains(err.Error(), occupied.Addr().String()) {
		t.Fatalf("Start error = %v, want occupied bind address", err)
	}
	if server.Addr() != nil {
		t.Fatal("failed Start retained listener")
	}
}

func TestServerReportsUnexpectedServeExit(t *testing.T) {
	server := NewServer("127.0.0.1:0", http.NotFoundHandler())
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	if err := server.listener.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-server.Done():
	case <-time.After(time.Second):
		t.Fatal("Done not closed after listener failure")
	}
	if err := server.Err(); err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Err = %v, want unexpected Serve failure", err)
	}
	if err := server.Start(); err == nil {
		t.Fatal("Start concealed previous Serve failure")
	}
	if err := server.Stop(context.Background()); err == nil {
		t.Fatal("Stop concealed Serve failure")
	}
}

func TestServerDrainsInFlightRequest(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		_, _ = io.WriteString(w, "complete")
	}))
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	responseBody := make(chan string, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://" + server.Addr().String())
		if err != nil {
			responseBody <- err.Error()
			return
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil {
			responseBody <- err.Error()
			return
		}
		responseBody <- string(body)
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	stopDone := make(chan error, 1)
	go func() { stopDone <- server.Stop(context.Background()) }()
	select {
	case err := <-stopDone:
		t.Fatalf("Stop completed before in-flight request: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-stopDone; err != nil {
		t.Fatal(err)
	}
	if body := <-responseBody; body != "complete" {
		t.Errorf("response body = %q, want complete", body)
	}
}

func TestServerShutdownRespectsContext(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	server := NewServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	requestDone := make(chan struct{})
	go func() {
		defer close(requestDone)
		client := &http.Client{Timeout: 2 * time.Second}
		response, err := client.Get("http://" + server.Addr().String())
		if err == nil {
			_ = response.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("request did not reach server")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := server.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Stop error = %v, want context deadline", err)
	}
	close(release)
	<-requestDone
}
