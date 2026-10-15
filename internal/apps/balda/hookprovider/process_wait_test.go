package hookprovider

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"
)

func TestExitingNativeParentHelper(t *testing.T) {
	if slices.Contains(os.Args, "orphan-native-child") {
		time.Sleep(time.Hour)
		os.Exit(0)
	}
	if !slices.Contains(os.Args, "exiting-native-parent") {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	child := exec.Command(executable, "-test.run=^TestExitingNativeParentHelper$", "--", "orphan-native-child")
	child.Stdout, child.Stderr = io.Discard, io.Discard
	if child.Start() != nil || json.NewEncoder(os.Stdout).Encode(child.Process.Pid) != nil {
		os.Exit(3)
	}
	os.Exit(0)
}

func TestWaitOwnedProcessCleansDescendantsAfterLeaderExit(t *testing.T) {
	if !nativePlatformSupported() {
		t.Skip("native process groups require Linux or macOS")
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, "-test.run=^TestExitingNativeParentHelper$", "--", "exiting-native-parent")
	configureOwnedProcess(command)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	defer func() {
		if !waited {
			_ = waitOwnedProcess(command)
		}
	}()
	var childPID int
	if err := json.NewDecoder(stdout).Decode(&childPID); err != nil {
		t.Fatal(err)
	}
	// EOF proves the leader closed stdout; the descendant owns no output pipe.
	if _, err := io.Copy(io.Discard, stdout); err != nil {
		t.Fatal(err)
	}
	child, err := readNativeProcess(childPID)
	if err != nil || childPID <= 1 || child.Group != command.Process.Pid {
		t.Fatalf("owned descendant before cleanup = %+v, %v", child, err)
	}
	_ = waitOwnedProcess(command)
	waited = true
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := readNativeProcess(childPID); err != nil {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("native descendant survived its leader and owned group cleanup")
}
