//go:build !windows

package balda

import (
	"errors"
	"syscall"
)

func startupProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
