package hookprovider

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

func TestNeutralProcessOwnerHelper(t *testing.T) {
	index := slices.Index(os.Args, "neutral-process-owner-helper")
	if index < 0 {
		return
	}
	path := os.Args[index+1]
	binding, err := ReadBinding(path)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "read binding %q from %q: %v\n", path, os.Args, err)
		os.Exit(3)
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	command := exec.CommandContext(context.Background(), executable, "-test.run=^TestNeutralLingeringChild$", "--", "neutral-lingering-child")
	ConfigureProcess(command)
	if err := command.Start(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "start child failed: %v\n", err)
		os.Exit(3)
	}
	if _, err := binding.RegisterProcess(t.Context(), command.Process.Pid); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "register child %d from %q: %v\n", command.Process.Pid, os.Args, err)
		_ = command.Process.Kill()
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]int{"pid": command.Process.Pid})
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestNeutralLingeringChild(t *testing.T) {
	if !slices.Contains(os.Args, "neutral-lingering-child") {
		return
	}
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestBindingReleaseTerminatesRegisteredDescendant(t *testing.T) {
	if !NativePlatformSupported() {
		t.Skip("process ownership requires Linux or macOS")
	}
	service, err := New(runnerFunc(func(context.Context, hookcmd.Event) (hookcmd.Result, error) {
		return hookcmd.Result{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	binding, err := service.Bind(t.Context(), BindingScope{SessionID: "session", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestNeutralProcessOwnerHelper$", "--", "neutral-process-owner-helper", binding.Reference(), binding.OwnerProof())
	command.Stderr = os.Stderr
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	})
	var child struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(stdout).Decode(&child); err != nil || child.PID <= 1 {
		t.Fatalf("registered child: pid=%d err=%v", child.PID, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := binding.Release(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !ProcessAlive(child.PID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("registered descendant survived binding release")
}

func TestUnregisteredProcessOwnerHelper(t *testing.T) {
	if !slices.Contains(os.Args, "unregistered-process-owner-helper") {
		return
	}
	executable, err := os.Executable()
	if err != nil {
		os.Exit(3)
	}
	command := exec.CommandContext(context.Background(), executable, "-test.run=^TestNeutralLingeringChild$", "--", "neutral-lingering-child")
	ConfigureProcess(command)
	if command.Start() != nil {
		os.Exit(3)
	}
	_ = json.NewEncoder(os.Stdout).Encode(map[string]int{"pid": command.Process.Pid})
	time.Sleep(time.Hour)
	os.Exit(0)
}

func TestBindingCannotClaimAnotherBindingsProcessOwner(t *testing.T) {
	if !NativePlatformSupported() {
		t.Skip("process ownership requires Linux or macOS")
	}
	service, err := New(runnerFunc(func(context.Context, hookcmd.Event) (hookcmd.Result, error) {
		return hookcmd.Result{}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	bindingA, err := service.Bind(t.Context(), BindingScope{SessionID: "session-a", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	bindingB, err := service.Bind(t.Context(), BindingScope{SessionID: "session-b", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ownerB := exec.Command(executable, "-test.run=^TestUnregisteredProcessOwnerHelper$", "--", "unregistered-process-owner-helper", bindingB.OwnerProof())
	ownerB.Stderr = os.Stderr
	stdout, err := ownerB.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := ownerB.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if ownerB.Process != nil {
			_ = ownerB.Process.Kill()
		}
		_ = ownerB.Wait()
	})
	var childB struct {
		PID int `json:"pid"`
	}
	if err := json.NewDecoder(stdout).Decode(&childB); err != nil || childB.PID <= 1 {
		t.Fatalf("binding B child: pid=%d err=%v", childB.PID, err)
	}
	request := processRequest{Operation: "attach", OwnerPID: ownerB.Process.Pid, ChildPID: childB.PID}
	if err := sendProcessMutation(t.Context(), *bindingA, request); err == nil {
		t.Fatal("binding A claimed binding B process owner")
	}
	if err := sendProcessMutation(t.Context(), *bindingB, request); err != nil {
		t.Fatalf("binding B could not register its process owner: %v", err)
	}
	if err := bindingA.Release(); err != nil {
		t.Fatal(err)
	}
	if !ProcessAlive(childB.PID) {
		t.Fatal("releasing binding A killed binding B child")
	}
	if err := ownerB.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = ownerB.Wait()
	if err := bindingB.Release(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if !ProcessAlive(childB.PID) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("releasing binding B did not kill its child")
}
