package hookprovider

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func nativePlatformSupported() bool { return true }

func readNativeProcess(pid int) (nativeProcessInfo, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	end := bytes.LastIndexByte(data, ')')
	if end < 0 {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	fields := strings.Fields(string(data[end+1:]))
	if len(fields) < 20 {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	group, err := strconv.Atoi(fields[2])
	if err != nil {
		return nativeProcessInfo{}, os.ErrProcessDone
	}
	return nativeProcessInfo{Parent: parent, Group: group, Start: fields[19]}, nil
}

func processHasArgument(pid int, expected string) (bool, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false, os.ErrProcessDone
	}
	for _, argument := range strings.Split(string(data), "\x00") {
		if argument == expected {
			return true, nil
		}
	}
	return false, nil
}

func killNativeGroup(pid int) error { return syscall.Kill(-pid, syscall.SIGKILL) }
