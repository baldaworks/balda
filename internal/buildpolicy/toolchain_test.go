package buildpolicy

import (
	"go/version"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

const minimumSecureGoVersion = "go1.26.10"

var (
	moduleGoVersion  = regexp.MustCompile(`(?m)^go ([0-9]+\.[0-9]+\.[0-9]+)$`)
	releaseGoVersion = regexp.MustCompile(`(?m)^ARG GO_IMAGE=golang:([0-9]+\.[0-9]+\.[0-9]+)-`)
)

func TestGoToolchainPolicy(t *testing.T) {
	repositoryRoot := filepath.Join("..", "..")
	moduleVersion := readVersion(t, filepath.Join(repositoryRoot, "go.mod"), moduleGoVersion)
	if version.Compare(moduleVersion, minimumSecureGoVersion) < 0 {
		t.Fatalf("go.mod version = %s, want %s or newer for GO-2026-6809", moduleVersion, minimumSecureGoVersion)
	}

	releaseVersion := readVersion(t, filepath.Join(repositoryRoot, "Dockerfile.release"), releaseGoVersion)
	if releaseVersion != moduleVersion {
		t.Fatalf("release image Go version = %s, want module version %s", releaseVersion, moduleVersion)
	}
}

func readVersion(t *testing.T, path string, pattern *regexp.Regexp) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	match := pattern.FindSubmatch(contents)
	if len(match) != 2 {
		t.Fatalf("read Go version from %s", path)
	}
	return "go" + string(match[1])
}
