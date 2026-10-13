package mcpfx

import "os"

// Terminating the owned shim closes its job and terminates its child tree.
func signalOwnedStdio(process *os.Process) error { return process.Kill() }
