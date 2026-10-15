//go:build !windows

package mcpfx

import (
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpcmd"
)

const diagnosticStdioMarker = "BALDA_DIAGNOSTIC_STDIO_STUBBORN"

func TestDiagnosticProbeReapsStubbornStdioBeforeReturn(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "pid")
	probe, err := NewDiagnosticProbe(
		diagnosticValues{values: mcpcmd.LaunchValues{Env: map[string]string{diagnosticStdioMarker: marker}}},
		diagnosticTokens{},
		NewClientLauncher(),
	)
	if err != nil {
		t.Fatal(err)
	}
	probe.timeout = 2 * time.Second
	probe.closeTimeout = 600 * time.Millisecond
	started := time.Now()
	result := probe.DiagnoseMCP(t.Context(), mcpcmd.Revision{
		ConnectionID: "stubborn-stdio",
		ID:           "exact-revision",
		Definition: mcpcmd.Definition{
			Transport: mcpcmd.TransportStdio,
			Command:   executable,
			Args:      []string{"-test.run=^TestDiagnosticStdioStubbornChild$"},
			Directory: t.TempDir(),
		},
	})
	if result.Outcome != mcpcmd.DiagnosticFailed || result.ToolCount != 0 {
		t.Fatalf("stubborn stdio diagnostic = %+v", result)
	}
	if elapsed := time.Since(started); elapsed > 3*time.Second {
		t.Fatalf("stubborn stdio cleanup exceeded diagnostic budget: %v", elapsed)
	}
	payload, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("stubborn child did not report its PID: %v", err)
	}
	pid, err := strconv.Atoi(string(payload))
	if err != nil {
		t.Fatalf("decode stubborn child PID: %v", err)
	}
	if stdioProcessAlive(pid) {
		t.Fatalf("stubborn stdio child %d remains alive after diagnostic return", pid)
	}
	var status syscall.WaitStatus
	if waited, err := syscall.Wait4(pid, &status, syscall.WNOHANG, nil); waited != -1 || !errors.Is(err, syscall.ECHILD) {
		t.Fatalf("stubborn stdio child was not reaped by cleanup: wait4=%d err=%v", waited, err)
	}
}

func TestDiagnosticStdioStubbornChild(t *testing.T) {
	marker := os.Getenv(diagnosticStdioMarker)
	if marker == "" {
		t.Skip("subprocess fixture")
	}
	signal.Ignore(syscall.SIGTERM)
	if err := os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}
