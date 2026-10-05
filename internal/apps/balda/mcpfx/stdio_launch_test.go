package mcpfx

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/mcpruntime"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/normahq/runtime/v2/mcpregistry"
)

func TestMain(m *testing.M) {
	if handled, code := RunStdioMode(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}

type stdioLaunchObservation struct {
	Directory string
	Args      []string
	Host      string
	Overlay   string
	PID       int
}

func TestStdioLaunchPreservesDirectoryArgumentsEnvironment(t *testing.T) {
	t.Setenv("BALDA_STDIO_LAUNCH_HOST", "inherited-host")
	t.Setenv("BALDA_STDIO_LAUNCH_OVERLAY", "parent-overlay")
	for _, source := range []runtimecatalogcmd.SourceKind{runtimecatalogcmd.SourceKindConfiguredMCP, runtimecatalogcmd.SourceKindManagedMCP} {
		t.Run(string(source), func(t *testing.T) {
			// Keep two servers alive at once: a session-wide chdir cannot satisfy
			// both of these independent directory contracts.
			for range 2 {
				directory := t.TempDir()
				literal := []string{"a b", "$HOME", "$(false)", `back\slash`, `quote"here`, "юникод"}
				command := stdioLaunchCommand(t, source, directory, literal, "")
				input, err := command.StdinPipe()
				if err != nil {
					t.Fatal(err)
				}
				output, err := command.StdoutPipe()
				if err != nil {
					t.Fatal(err)
				}
				if err := command.Start(); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					_ = input.Close()
					if err := command.Wait(); err != nil {
						t.Errorf("stdio EOF did not close the owned process: %v", err)
					}
				})
				var got stdioLaunchObservation
				if err := json.NewDecoder(output).Decode(&got); err != nil {
					t.Fatal(err)
				}
				actual, err := os.Stat(got.Directory)
				if err != nil {
					t.Fatal(err)
				}
				expected, err := os.Stat(directory)
				if err != nil {
					t.Fatal(err)
				}
				if !os.SameFile(actual, expected) || !slices.Equal(got.Args, literal) || got.Host != "inherited-host" || got.Overlay != "resolved-overlay" {
					t.Fatalf("independent stdio launch contract lost: %+v", got)
				}
				if runtime.GOOS != "windows" && got.PID != command.Process.Pid {
					t.Fatal("Unix stdio launch did not replace the wrapper process")
				}
			}
		})
	}
}

func TestStdioLaunchResolvesHostPATHAndRelativeChildPaths(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	t.Chdir(root)
	directory := filepath.Join(root, "tools")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	name, err := filepath.Rel(directory, executable)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", filepath.Dir(executable))
	for _, command := range []string{filepath.Base(executable), name, executable} {
		gotDir, gotExecutable, err := resolveStdioLaunch(command, "tools")
		if err != nil || gotDir != directory || gotExecutable != executable {
			t.Fatalf("launch resolution drifted from direct os/exec: %q -> %q, %q, %v", command, gotDir, gotExecutable, err)
		}
	}
}

func TestStdioLaunchRejectsUnavailableDirectoryAndExecutable(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	file := filepath.Join(directory, "not-directory")
	if err := os.WriteFile(file, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ command, directory string }{
		{executable, filepath.Join(directory, "missing")},
		{executable, file},
		{filepath.Join(directory, "missing-command"), directory},
		{"", directory},
	} {
		registry := mcpregistry.New(nil)
		projector, err := NewRegistryProjector(registry)
		if err != nil {
			t.Fatal(err)
		}
		key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: runtimecatalogcmd.SourceKindManagedMCP}, Name: "bad-launch"}
		if _, err := projector.Project(t.Context(), key, mcpruntime.LaunchConfig{Transport: transportStdio, Command: test.command, WorkingDir: test.directory}); err == nil {
			t.Fatal("invalid native launch silently projected or fell back")
		}
		if _, found := registry.Get(RegistryID(key)); found {
			t.Fatal("failed launch changed the provider registry")
		}
	}
}

func TestStdioLaunchPreservesExitStatus(t *testing.T) {
	command := stdioLaunchCommand(t, runtimecatalogcmd.SourceKindConfiguredMCP, t.TempDir(), nil, "27")
	command.Stderr = nil
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 27 {
		t.Fatalf("child exit status lost: %v, output %s", err, output)
	}
}

func TestStdioLaunchForcedTerminationLeavesNoChild(t *testing.T) {
	testStdioTermination(t, false)
}

func TestStdioLaunchContextCancellationLeavesNoChild(t *testing.T) {
	testStdioTermination(t, true)
}

func testStdioTermination(t *testing.T, cancellation bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := stdioLaunchCommandContext(t, ctx, runtimecatalogcmd.SourceKindManagedMCP, t.TempDir(), nil, "")
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = input.Close() }()
	output, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = command.Process.Kill() }()
	var got stdioLaunchObservation
	if err := json.NewDecoder(output).Decode(&got); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if stdioProcessAlive(got.PID) {
			child, err := os.FindProcess(got.PID)
			if err == nil {
				_ = child.Kill()
			}
		}
	})
	if cancellation {
		cancel()
	} else {
		if err := command.Process.Kill(); err != nil {
			t.Fatal(err)
		}
	}
	_ = command.Wait()
	deadline := time.Now().Add(5 * time.Second)
	for stdioProcessAlive(got.PID) {
		if time.Now().After(deadline) {
			t.Fatal("forced wrapper termination orphaned its stdio child")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func stdioLaunchCommand(t *testing.T, source runtimecatalogcmd.SourceKind, directory string, literal []string, exit string) *exec.Cmd {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	return stdioLaunchCommandContext(t, ctx, source, directory, literal, exit)
}

func stdioLaunchCommandContext(t *testing.T, ctx context.Context, source runtimecatalogcmd.SourceKind, directory string, literal []string, exit string) *exec.Cmd {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	registry := mcpregistry.New(nil)
	projector, err := NewRegistryProjector(registry)
	if err != nil {
		t.Fatal(err)
	}
	key := mcpruntime.InstanceKey{Source: runtimecatalogcmd.SourceID{Kind: source, Name: "native"}, Revision: "one", Name: "native"}
	config := mcpruntime.LaunchConfig{Transport: transportStdio, Command: executable,
		Args: append([]string{"-test.run=^TestMCPStdioLaunchChild$", "--"}, literal...), WorkingDir: directory, InheritEnvironment: true,
		Env: map[string]string{"BALDA_STDIO_LAUNCH_CHILD": "1", "BALDA_STDIO_LAUNCH_OVERLAY": "resolved-overlay", "BALDA_STDIO_LAUNCH_EXIT": exit}}
	if _, err := projector.Project(t.Context(), key, config); err != nil {
		t.Fatal(err)
	}
	projected, found := registry.Get(RegistryID(key))
	if !found {
		t.Fatal("stdio projection unavailable")
	}
	command := exec.CommandContext(ctx, projected.Cmd[0], projected.Args...)
	command.Dir = t.TempDir() // ACP only supplies this session directory.
	command.Env = append(os.Environ(), environment(projected.Env)...)
	command.Stderr = os.Stderr
	return command
}

func TestMCPStdioLaunchChild(t *testing.T) {
	if os.Getenv("BALDA_STDIO_LAUNCH_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	directory, err := os.Getwd()
	if err != nil {
		os.Exit(1)
	}
	got := stdioLaunchObservation{Directory: directory, Args: os.Args[3:], Host: os.Getenv("BALDA_STDIO_LAUNCH_HOST"), Overlay: os.Getenv("BALDA_STDIO_LAUNCH_OVERLAY"), PID: os.Getpid()}
	if err := json.NewEncoder(os.Stdout).Encode(got); err != nil {
		os.Exit(1)
	}
	if value := os.Getenv("BALDA_STDIO_LAUNCH_EXIT"); value != "" {
		code, _ := strconv.Atoi(value)
		os.Exit(code)
	}
	_, _ = io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}
