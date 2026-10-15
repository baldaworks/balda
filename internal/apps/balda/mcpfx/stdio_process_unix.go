//go:build !windows

package mcpfx

import "os"

func signalOwnedStdio(process *os.Process) error { return process.Signal(os.Interrupt) }
