//go:build unix

package main

import (
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
)

func TestUnixPreflightChecksCoreBeforeBrowserBind(t *testing.T) {
	dir, err := os.MkdirTemp("", "balda-edge-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "core.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"contract_id":"mismatch","contract_version":%q,"api_major":%d}`, managementv1.ContractVersion, managementv1.APIMajor)
	}), ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	browser, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := browser.Addr().String()
	_ = browser.Close()
	config := "balda:\n  http:\n    base_url: http://127.0.0.1:8095\n  backoffice:\n    listen_addr: " + address + "\n  management:\n    enabled: true\n    socket_path: " + path + "\n    operator_token: local-token\n"
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	opts := options{configDir: dir}
	if _, err := prepare(t.Context(), opts, false); err != nil {
		t.Fatalf("offline socket validation: %v", err)
	}
	if err := start(t.Context(), opts); err == nil || !strings.Contains(err.Error(), "incompatible") {
		t.Fatalf("start error = %v, want incompatible Core", err)
	}
	browser, err = net.Listen("tcp", address)
	if err != nil {
		t.Fatalf("browser bound before Unix compatibility preflight: %v", err)
	}
	_ = browser.Close()
}
