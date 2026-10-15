package catalogapp

import (
	"errors"

	"golang.org/x/sys/windows"
)

func catalogProcessAlive(pid int) (bool, error) {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer func() { _ = windows.CloseHandle(process) }()
	status, err := windows.WaitForSingleObject(process, 0)
	return status == uint32(windows.WAIT_TIMEOUT), err
}
