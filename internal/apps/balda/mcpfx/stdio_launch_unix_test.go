//go:build !windows

package mcpfx

import "syscall"

func stdioProcessAlive(pid int) bool { return syscall.Kill(pid, 0) == nil }
