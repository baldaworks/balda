//go:build !windows

package catalogapp

import (
	"errors"
	"syscall"
)

func catalogProcessAlive(pid int) (bool, error) {
	err := syscall.Kill(pid, 0)
	if errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	return err == nil, err
}
