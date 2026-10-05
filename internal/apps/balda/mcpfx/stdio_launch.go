package mcpfx

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
)

const stdioMode = "--balda-mcp-stdio"

// RunStdioMode dispatches the private provider launch tuple before normal CLI
// startup. It carries no configuration, database, or authorization policy.
func RunStdioMode(args []string) (bool, int) {
	if len(args) == 0 || args[0] != stdioMode {
		return false, 0
	}
	if len(args) < 3 || !filepath.IsAbs(args[1]) || !filepath.IsAbs(args[2]) {
		fmt.Fprintln(os.Stderr, "Error: invalid MCP stdio launch")
		return true, 1
	}
	code, err := runStdioProcess(args[1], args[2], args[3:])
	if err != nil {
		// Native errors can include command arguments. Keep this boundary
		// bounded; protected values must never become CLI diagnostics.
		fmt.Fprintln(os.Stderr, "Error: MCP stdio launch failed")
		return true, 1
	}
	return true, code
}

func stdioProviderLaunch(config mcpruntime.LaunchConfig) (mcpruntime.LaunchConfig, error) {
	directory, executable, err := resolveStdioLaunch(config.Command, config.WorkingDir)
	if err != nil {
		return mcpruntime.LaunchConfig{}, err
	}
	balda, err := os.Executable()
	if err != nil {
		return mcpruntime.LaunchConfig{}, errors.New("MCP stdio host executable is unavailable")
	}
	config.Command = balda
	config.Args = append([]string{stdioMode, directory, executable}, config.Args...)
	config.WorkingDir = directory
	return config, nil
}

// Match os/exec: a bare command uses the host PATH before any chdir; a
// relative path with separators executes relative to the configured directory.
// Both direct discovery and provider projection resolve here, never in the
// provider session's directory or against its environment overlay.
func resolveStdioLaunch(name, directory string) (string, string, error) {
	directory, err := filepath.Abs(directory)
	if err != nil {
		return "", "", errors.New("MCP stdio directory is invalid")
	}
	info, err := os.Stat(directory)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("MCP stdio directory is unavailable")
	}
	command := exec.Command(name)
	if command.Err != nil {
		return "", "", errors.New("MCP stdio executable is unavailable")
	}
	path, err := stdioExecutablePath(directory, command.Path)
	if err != nil {
		return "", "", errors.New("MCP stdio executable is unavailable")
	}
	return directory, path, nil
}
