//go:build unix

package managementclient

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestUnixClientAuthCompatibilityAndNoTCPFallback(t *testing.T) {
	dir, err := os.MkdirTemp("", "balda-client-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	var tcpCalls atomic.Int32
	tcp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tcpCalls.Add(1)
		compatibilityBody(managementv1.ContractID, managementv1.ContractVersion, managementv1.APIMajor)(w)
	}))
	t.Cleanup(tcp.Close)
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	var mismatch atomic.Bool
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer local-token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if mismatch.Load() {
			compatibilityBody("other-contract", managementv1.ContractVersion, managementv1.APIMajor)(w)
			return
		}
		compatibilityBody(managementv1.ContractID, managementv1.ContractVersion, managementv1.APIMajor)(w)
	}), ReadHeaderTimeout: requestTimeout}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	// Even an explicitly supplied TCP transport must not bypass socket selection.
	client, err := New(Config{BaseURL: tcp.URL, SocketPath: path, OperatorToken: "local-token", HTTPClient: tcp.Client()})
	if err != nil {
		t.Fatal(err)
	}
	if err := client.Compatibility(t.Context()); err != nil {
		t.Fatal(err)
	}
	wrongToken, err := New(Config{BaseURL: tcp.URL, SocketPath: path, OperatorToken: "wrong-token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := wrongToken.Compatibility(t.Context()); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("authentication error = %v", err)
	}
	mismatch.Store(true)
	if err := client.Compatibility(t.Context()); !errors.Is(err, ErrIncompatible) {
		t.Fatalf("compatibility error = %v", err)
	}
	if err := server.Close(); err != nil {
		t.Fatal(err)
	}
	if err := client.Compatibility(t.Context()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("missing socket error = %v", err)
	}
	if got := tcpCalls.Load(); got != 0 {
		t.Fatalf("socket client made %d TCP requests", got)
	}
}
