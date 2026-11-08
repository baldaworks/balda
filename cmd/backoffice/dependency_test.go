package main

import (
	"os/exec"
	"strings"
	"testing"
)

func TestStandaloneDependencyBoundary(t *testing.T) {
	command := exec.Command("go", "list", "-deps", ".")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("go list: %v: %s", err, output)
	}
	for _, dependency := range strings.Fields(string(output)) {
		if forbiddenDependency(dependency) {
			t.Errorf("forbidden standalone dependency: %s", dependency)
		}
	}
}

func forbiddenDependency(dependency string) bool {
	if dependency == "database/sql" || strings.Contains(dependency, "sqlite") || strings.Contains(dependency, "pgx") {
		return true
	}
	for _, prefix := range []string{
		"github.com/baldaworks/balda/internal/apps/balda/",
		"github.com/baldaworks/balda/internal/apps/backoffice/security",
		"github.com/baldaworks/balda/internal/apps/backoffice/access",
		"github.com/baldaworks/balda/internal/apps/backoffice/audit",
	} {
		if strings.HasPrefix(dependency, prefix) {
			return true
		}
	}
	return dependency == "github.com/baldaworks/balda/internal/apps/backoffice"
}
