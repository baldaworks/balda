package hookprovider

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func nativePlatformSupported() bool { return true }

func readNativeProcess(pid int) (nativeProcessInfo, error) {
	info, err := unix.SysctlKinfoProc("kern.proc.pid", pid)
	if err != nil || info.Proc.P_pid != int32(pid) {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	return nativeProcessInfo{Parent: int(info.Eproc.Ppid), Group: int(info.Eproc.Pgid), Start: fmt.Sprintf("%d:%d", info.Proc.P_starttime.Sec, info.Proc.P_starttime.Usec)}, nil
}

func proxyOwnsBinding(pid int, path string) (bool, error) {
	data, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(data) < 4 {
		return false, os.ErrProcessDone
	}
	argc := int(binary.LittleEndian.Uint32(data[:4]))
	data = data[4:]
	end := bytes.IndexByte(data, 0)
	if end < 0 {
		return false, os.ErrProcessDone
	}
	data = data[end+1:]
	for len(data) > 0 && data[0] == 0 {
		data = data[1:]
	}
	var args []string
	for i := 0; i < argc; i++ {
		end = bytes.IndexByte(data, 0)
		if end < 0 {
			return false, os.ErrProcessDone
		}
		args = append(args, string(data[:end]))
		data = data[end+1:]
	}
	return bindingArgumentsMatch(args, path), nil
}

func killNativeGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }
