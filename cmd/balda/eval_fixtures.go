package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const defaultScenarioDir = "testdata/scenarios"

func evalFixturesCommand() *cobra.Command {
	var scenarioDir string
	var scenarioName string
	var actualEventsPath string

	cmd := &cobra.Command{
		Use:   "eval-fixtures",
		Short: "Validate scenario fixtures and compare actual events against golden",
		RunE: func(_ *cobra.Command, _ []string) error {
			files, err := scenarioFixtureFiles(scenarioDir)
			if err != nil {
				return err
			}
			if scenarioName != "" {
				filtered, ok := files[scenarioName]
				if !ok {
					return fmt.Errorf("scenario %q not found in %s", scenarioName, scenarioDir)
				}
				files = map[string]scenarioFixture{scenarioName: filtered}
			}
			names := make([]string, 0, len(files))
			for name := range files {
				names = append(names, name)
			}
			slices.Sort(names)
			for _, name := range names {
				fixture := files[name]
				if err := validateFixtureFile(fixture.ScenarioPath); err != nil {
					return err
				}
				if _, err := loadGoldenEvents(fixture.GoldenPath); err != nil {
					return err
				}
			}

			if strings.TrimSpace(actualEventsPath) != "" {
				if len(names) != 1 {
					return fmt.Errorf("--actual-events requires exactly one scenario; set --scenario")
				}
				golden, err := loadGoldenEvents(files[names[0]].GoldenPath)
				if err != nil {
					return err
				}
				actual, err := loadGoldenEvents(actualEventsPath)
				if err != nil {
					return fmt.Errorf("load actual events: %w", err)
				}
				if err := compareEventTypes(golden, actual); err != nil {
					return err
				}
			}

			if _, err := fmt.Fprintf(os.Stdout, "validated %d scenario fixture(s)\n", len(names)); err != nil {
				return fmt.Errorf("write summary: %w", err)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&scenarioDir, "scenarios-dir", defaultScenarioDir, "scenario fixture directory")
	cmd.Flags().StringVar(&scenarioName, "scenario", "", "scenario name to validate (without extension)")
	cmd.Flags().StringVar(&actualEventsPath, "actual-events", "", "path to actual events JSON for golden comparison")
	return cmd
}

type scenarioFixture struct {
	ScenarioPath string
	GoldenPath   string
}

func scenarioFixtureFiles(dir string) (map[string]scenarioFixture, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read scenarios dir %q: %w", dir, err)
	}
	fixtures := make(map[string]scenarioFixture)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		ext := strings.ToLower(filepath.Ext(name))
		if ext != ".json" && ext != ".yaml" && ext != ".yml" {
			continue
		}
		base := strings.TrimSuffix(name, ext)
		goldenPath := filepath.Join(dir, "golden", base+".events.golden.json")
		fixtures[base] = scenarioFixture{
			ScenarioPath: filepath.Join(dir, name),
			GoldenPath:   goldenPath,
		}
	}
	if len(fixtures) == 0 {
		return nil, fmt.Errorf("no scenario files found in %s", dir)
	}
	return fixtures, nil
}

func validateFixtureFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read scenario %s: %w", path, err)
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		if !json.Valid(data) {
			return fmt.Errorf("scenario %s is not valid JSON", path)
		}
	case ".yaml", ".yml":
		var decoded any
		if err := yaml.Unmarshal(data, &decoded); err != nil {
			return fmt.Errorf("scenario %s is not valid YAML: %w", path, err)
		}
	default:
		return fmt.Errorf("unsupported scenario extension for %s", path)
	}
	return nil
}

func loadGoldenEvents(path string) ([]map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read golden events %s: %w", path, err)
	}
	var events []map[string]any
	if err := json.Unmarshal(data, &events); err != nil {
		return nil, fmt.Errorf("parse golden events %s: %w", path, err)
	}
	if len(events) == 0 {
		return nil, fmt.Errorf("golden events %s is empty", path)
	}
	for i, event := range events {
		if strings.TrimSpace(fmt.Sprintf("%v", event["type"])) == "" {
			return nil, fmt.Errorf("golden events %s entry %d missing type", path, i)
		}
	}
	return events, nil
}

func compareEventTypes(golden, actual []map[string]any) error {
	if len(golden) != len(actual) {
		return fmt.Errorf("actual event count %d does not match golden %d", len(actual), len(golden))
	}
	for i := range golden {
		goldenType := strings.TrimSpace(fmt.Sprintf("%v", golden[i]["type"]))
		actualType := strings.TrimSpace(fmt.Sprintf("%v", actual[i]["type"]))
		if goldenType != actualType {
			return fmt.Errorf("event[%d] type = %q, want %q", i, actualType, goldenType)
		}
	}
	return nil
}
