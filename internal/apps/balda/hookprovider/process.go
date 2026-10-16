package hookprovider

import (
	"os/exec"
)

// NativePlatformSupported reports whether descendant process ownership can be enforced.
func NativePlatformSupported() bool { return nativePlatformSupported() }

// ConfigureProcess isolates a binding-owned child process for group cleanup.
func ConfigureProcess(command *exec.Cmd) { configureOwnedProcess(command) }

// WaitProcess terminates and reaps a binding-owned child process when needed.
func WaitProcess(command *exec.Cmd) error { return waitOwnedProcess(command) }

// ProcessAlive reports whether a process identity is still observable.
func ProcessAlive(pid int) bool {
	_, err := readNativeProcess(pid)
	return err == nil
}
