//go:build !windows

package runtimecatalog

import (
	"syscall"
	"testing"
)

func makeSpecialFile(t *testing.T, path string) {
	t.Helper()
	if err := syscall.Mkfifo(path, 0o600); err != nil {
		t.Skipf("Mkfifo: %v", err)
	}
}
