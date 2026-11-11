package main

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	managementv1 "github.com/baldaworks/balda/internal/managementapi/v1"
	"github.com/stretchr/testify/require"
)

func TestStartChecksCompatibilityBeforeBrowserBind(t *testing.T) {
	core := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"contract_id":"wrong","contract_version":%q,"api_major":%d}`,
			managementv1.ContractVersion, managementv1.APIMajor)
	}))
	t.Cleanup(core.Close)
	coreURL, err := url.Parse(core.URL)
	require.NoError(t, err)

	probe, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	browserAddr := probe.Addr().String()
	require.NoError(t, probe.Close())

	directory := t.TempDir()
	config := strings.Join([]string{
		"runtime:",
		"  providers:",
		"    agent:",
		"      type: opencode_acp",
		"      opencode_acp:",
		"        model: test-model",
		"balda:",
		"  http:",
		"    base_url: http://127.0.0.1:8095",
		"    base_path: ''",
		"  backoffice:",
		"    listen_addr: " + browserAddr,
		"  management:",
		"    enabled: true",
		"    listen_addr: " + coreURL.Host,
		"    operator_token: test-token",
	}, "\n") + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "config.yaml"), []byte(config), 0o600))

	err = start(t.Context(), options{configDir: directory})
	require.ErrorContains(t, err, "incompatible")

	listener, err := net.Listen("tcp", browserAddr)
	require.NoError(t, err, "browser listener must not bind before compatibility succeeds")
	require.NoError(t, listener.Close())
}
