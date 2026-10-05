package mcpfx

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

func TestStdioLaunchWindowsRelativePathsMatchDirectExecution(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	relative, err := filepath.Rel(directory, executable)
	if err != nil {
		t.Fatal(err)
	}
	volume := filepath.VolumeName(executable)
	for _, path := range []string{strings.TrimPrefix(executable, volume), volume + relative} {
		t.Run(path, func(t *testing.T) {
			_, resolved, err := resolveStdioLaunch(path, directory)
			if err != nil {
				t.Fatal(err)
			}
			for _, commandPath := range []string{path, resolved} {
				command := exec.CommandContext(t.Context(), commandPath, "-test.run=^TestMCPStdioLaunchChild$", "--")
				command.Dir = directory
				command.Env = append(os.Environ(), "BALDA_STDIO_LAUNCH_CHILD=1", "BALDA_STDIO_LAUNCH_EXIT=0")
				output, err := command.Output()
				if err != nil {
					t.Fatalf("native path %q: %v", commandPath, err)
				}
				var got stdioLaunchObservation
				if err := json.Unmarshal(output, &got); err != nil {
					t.Fatal(err)
				}
				if got.Directory != directory {
					t.Fatal("native executable path lost its child directory")
				}
			}
		})
	}
}

func TestStdioLaunchWindowsPreservesRecognizedExtension(t *testing.T) {
	t.Setenv("PATHEXT", ".EXE")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "server.exe.exe"), data, 0700); err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), `.\server.exe`, "-test.run=^TestMCPStdioLaunchChild$", "--")
	command.Dir = directory
	command.Env = append(os.Environ(), "BALDA_STDIO_LAUNCH_CHILD=1", "BALDA_STDIO_LAUNCH_EXIT=0")
	if err := command.Run(); err == nil {
		t.Fatal("direct native execution unexpectedly selected the suffixed neighbor")
	}
	if _, _, err := resolveStdioLaunch(`.\server.exe`, directory); err == nil {
		t.Fatal("adapter selected a different executable after direct native execution failed")
	}
}

func stdioProcessAlive(pid int) bool {
	process, err := windows.OpenProcess(windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		return false
	}
	defer func() { _ = windows.CloseHandle(process) }()
	status, err := windows.WaitForSingleObject(process, 0)
	return err == nil && status == uint32(windows.WAIT_TIMEOUT)
}
