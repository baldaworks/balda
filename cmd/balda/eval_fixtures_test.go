package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestScenarioFixtureFiles(t *testing.T) {
	dir := t.TempDir()
	goldenDir := filepath.Join(dir, "golden")
	if err := os.MkdirAll(goldenDir, 0o755); err != nil {
		t.Fatalf("mkdir golden: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "webhook_task.json"), []byte(`{"name":"webhook_task"}`), 0o600); err != nil {
		t.Fatalf("write scenario: %v", err)
	}
	if err := os.WriteFile(filepath.Join(goldenDir, "webhook_task.events.golden.json"), []byte(`[{"type":"command.accepted"}]`), 0o600); err != nil {
		t.Fatalf("write golden: %v", err)
	}

	fixtures, err := scenarioFixtureFiles(dir)
	if err != nil {
		t.Fatalf("scenarioFixtureFiles() error = %v", err)
	}
	if len(fixtures) != 1 {
		t.Fatalf("len(fixtures) = %d, want 1", len(fixtures))
	}
	if fixtures["webhook_task"].GoldenPath == "" {
		t.Fatal("GoldenPath is empty")
	}
}

func TestValidateFixtureFile(t *testing.T) {
	dir := t.TempDir()
	jsonPath := filepath.Join(dir, "scenario.json")
	yamlPath := filepath.Join(dir, "scenario.yaml")
	badPath := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(jsonPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write json: %v", err)
	}
	if err := os.WriteFile(yamlPath, []byte("name: test\n"), 0o600); err != nil {
		t.Fatalf("write yaml: %v", err)
	}
	if err := os.WriteFile(badPath, []byte(`{`), 0o600); err != nil {
		t.Fatalf("write bad json: %v", err)
	}

	if err := validateFixtureFile(jsonPath); err != nil {
		t.Fatalf("validateFixtureFile(json) error = %v", err)
	}
	if err := validateFixtureFile(yamlPath); err != nil {
		t.Fatalf("validateFixtureFile(yaml) error = %v", err)
	}
	if err := validateFixtureFile(badPath); err == nil {
		t.Fatal("validateFixtureFile(bad) error = nil, want non-nil")
	}
}

func TestCompareEventTypes(t *testing.T) {
	golden := []map[string]any{
		{"type": "command.accepted"},
		{"type": "job.created"},
	}
	actual := []map[string]any{
		{"type": "command.accepted"},
		{"type": "job.created"},
	}
	if err := compareEventTypes(golden, actual); err != nil {
		t.Fatalf("compareEventTypes() error = %v", err)
	}

	actual[1]["type"] = "job.failed"
	err := compareEventTypes(golden, actual)
	if err == nil {
		t.Fatal("compareEventTypes() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "want") {
		t.Fatalf("compareEventTypes() error = %v, want mismatch marker", err)
	}
}
