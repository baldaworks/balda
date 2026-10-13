package mcpfx

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"path/filepath"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

func TestOwnedStdioHealthyDiscoverySurvivesSetupCancellation(t *testing.T) {
	t.Setenv("BALDA_MCP_STDIO_HOST_FIXTURE", "host-present")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	process, err := NewClientLauncher().Launch(t.Context(), mcpruntime.InstanceKey{Name: "worker"}, mcpruntime.LaunchConfig{Transport: transportStdio, Command: executable, WorkingDir: t.TempDir(), InheritEnvironment: true,
		Args: []string{"-test.run=^TestMCPStdioEnvironmentChild$"}, Env: map[string]string{"BALDA_MCP_STDIO_CHILD_FIXTURE": "1", "BALDA_MCP_STDIO_OVERLAY_FIXTURE": "child-value"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = process.Stop(ctx)
	})
	setup, cancel := context.WithTimeout(t.Context(), time.Second)
	tools, err := process.Discover(setup)
	cancel()
	if err != nil || len(tools) != 1 || tools[0].Description != "host-present:child-value" {
		t.Fatalf("owned stdio discovery = %v/%v", tools, err)
	}
	select {
	case <-process.Done():
		t.Fatal("setup cancellation killed healthy owned process")
	default:
	}
	ctx, stopCancel := context.WithTimeout(t.Context(), time.Second)
	defer stopCancel()
	if err := process.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-process.Done():
	default:
		t.Fatal("Stop returned before child reap")
	}
	if err := process.(*stdioProcess).command.Process.Kill(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("child was not reaped: %v", err)
	}
}

func TestOwnedStdioHungHandshakeAndStubbornChildAreStoppedAndReaped(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "ready")
	process, err := NewClientLauncher().Launch(t.Context(), mcpruntime.InstanceKey{Name: "stubborn"}, mcpruntime.LaunchConfig{Transport: transportStdio, Command: executable, WorkingDir: t.TempDir(),
		Args: []string{"-test.run=^TestOwnedStdioStubbornChild$"}, Env: map[string]string{"BALDA_STDIO_STUBBORN_CHILD": marker}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = process.Stop(ctx)
	})
	readyCtx, cancelReady := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancelReady()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		select {
		case <-tick.C:
		case <-readyCtx.Done():
			t.Fatal("stubborn child did not report ready")
		}
	}
	setup, cancel := context.WithTimeout(t.Context(), 35*time.Millisecond)
	started := time.Now()
	_, err = process.Discover(setup)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(started) > 250*time.Millisecond {
		t.Fatalf("hung setup deadline=%v duration=%v", err, time.Since(started))
	}
	stopCtx, cancelStop := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancelStop()
	err = process.Stop(stopCtx)
	if err != nil && !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded stop = %v", err)
	}
	select {
	case <-process.Done():
	case <-time.After(time.Second):
		t.Fatal("forced termination did not reap child")
	}
	select {
	case <-process.(*stdioProcess).stopped:
	case <-time.After(time.Second):
		t.Fatal("SDK cleanup did not drain after child reap")
	}
	if err := process.(*stdioProcess).command.Process.Kill(); !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("owned child remains unreaped: %v", err)
	}
}

func TestOwnedStdioStubbornChild(t *testing.T) {
	marker := os.Getenv("BALDA_STDIO_STUBBORN_CHILD")
	if marker == "" {
		t.Skip("subprocess fixture")
	}
	signal.Ignore(os.Interrupt)
	if err := os.WriteFile(marker, []byte("ready"), 0600); err != nil {
		t.Fatal(err)
	}
	for {
		time.Sleep(time.Hour)
	}
}

func TestOwnedStdioPreCanceledLaunchDoesNotStartChild(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	process, err := NewClientLauncher().Launch(ctx, mcpruntime.InstanceKey{}, mcpruntime.LaunchConfig{Transport: transportStdio, Command: "unavailable"})
	if process != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled launch=%v/%v", process, err)
	}
}
