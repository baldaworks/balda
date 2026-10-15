package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"slices"
	"testing"
)

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "--balda-mcp-stdio" {
		main()
	}
	os.Exit(m.Run())
}

func TestPrivateStdioLaunchPrecedesCLIStartup(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	literal := []string{"literal $HOME", `quote" and \slash`, "$(false)"}
	args := append([]string{"--balda-mcp-stdio", directory, executable, "-test.run=^TestPrivateStdioChild$", "--"}, literal...)
	command := exec.CommandContext(t.Context(), executable, args...)
	command.Env = append(os.Environ(), "BALDA_STDIO_ENTRYPOINT_CHILD=1")
	command.Dir = t.TempDir()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("private launch attempted ordinary CLI/config startup: %v: %s", err, output)
	}
	var got struct {
		Directory string
		Args      []string
	}
	if err := json.Unmarshal(output, &got); err != nil {
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
	if !os.SameFile(actual, expected) || !slices.Equal(got.Args, literal) {
		t.Fatalf("private launch lost literal arguments or cwd: %+v", got)
	}
}

func TestPrivateStdioChild(t *testing.T) {
	if os.Getenv("BALDA_STDIO_ENTRYPOINT_CHILD") != "1" {
		t.Skip("subprocess fixture")
	}
	directory, err := os.Getwd()
	if err != nil {
		os.Exit(1)
	}
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Directory string
		Args      []string
	}{directory, os.Args[3:]}); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
}
