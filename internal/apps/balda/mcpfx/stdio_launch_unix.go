//go:build !windows

package mcpfx

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

func stdioExecutablePath(directory, executable string) (string, error) {
	if !filepath.IsAbs(executable) {
		executable = filepath.Join(directory, executable)
	}
	return exec.LookPath(executable)
}

func runStdioProcess(directory, executable string, args []string) (int, error) {
	if err := os.Chdir(directory); err != nil {
		return 1, err
	}
	// Replacement preserves the provider-owned PID, stdio and signal lifetime.
	return 1, syscall.Exec(executable, append([]string{executable}, args...), os.Environ())
}
