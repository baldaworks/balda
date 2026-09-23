package backoffice

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func TestRuntimeSharedProviderLifecycle(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	config.Server.ListenAddr = listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("Start() succeeded before administrator bootstrap")
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), BootstrapInput{Username: "admin", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+config.Server.ListenAddr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("health status = %d, want 200", response.StatusCode)
	}
	stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Users().ListUsers(t.Context(), usercmd.PageRequest{Limit: 1}); err != nil {
		t.Fatalf("provider closed by Backoffice Stop: %v", err)
	}
}

func TestRuntimeStartReturnsBindFailure(t *testing.T) {
	provider, config := newHTTPAppTestState(t)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	config.Server.ListenAddr = listener.Addr().String()
	runtime, err := NewRuntime(config, provider)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.BootstrapAdmin(t.Context(), BootstrapInput{Username: "admin", Password: []byte("correct horse battery staple")}); err != nil {
		t.Fatal(err)
	}
	if err := runtime.Start(t.Context()); err == nil {
		t.Fatal("Start() succeeded with occupied listener")
	}
	if runtime.Done() != nil {
		t.Fatal("serving loop started despite bind failure")
	}
}

func TestHealthHandlerIsNonSensitiveAndNoStore(t *testing.T) {
	t.Parallel()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	recorder := httptest.NewRecorder()
	healthHandler().ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok\n" {
		t.Fatalf("health response = %d %q", recorder.Code, recorder.Body.String())
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", recorder.Header().Get("Cache-Control"))
	}
}
