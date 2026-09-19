package main

import (
	"fmt"

	"github.com/baldaworks/balda/internal/apps/balda/paths"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/normahq/runtime/v2/appconfig"
	"gopkg.in/yaml.v3"
)

const databaseTypeSQLite = "sqlite"

func resolveBaldaInitDatabase(workingDir string, document map[string]any) (string, state.DatabaseConfig, error) {
	defaults, err := yaml.Marshal(document)
	if err != nil {
		return "", state.DatabaseConfig{}, fmt.Errorf("marshal initialization configuration: %w", err)
	}
	// Initialization resolves state settings without validating generated agent
	// configuration; runtime validation remains the responsibility of start/validate.
	settings, _, err := appconfig.LoadResolvedSettings(
		appconfig.RuntimeLoadOptions{WorkingDir: workingDir, ConfigDir: configDir, Profile: profile},
		appconfig.AppLoadOptions{AppName: "balda", DefaultsYAML: defaults, UseDotConfigAppDir: true},
	)
	if err != nil {
		return "", state.DatabaseConfig{}, err
	}
	var effective baldaConfigDocument
	if err := appconfig.DecodeSettings(settings, &effective); err != nil {
		return "", state.DatabaseConfig{}, fmt.Errorf("decode initialization configuration: %w", err)
	}
	stateWorkingDir, err := paths.ResolveWorkingDir(effective.Balda.WorkingDir)
	if err != nil {
		return "", state.DatabaseConfig{}, err
	}
	stateDir, err := paths.ResolveStateDir(stateWorkingDir, effective.Balda.StateDir)
	if err != nil {
		return "", state.DatabaseConfig{}, err
	}
	database, err := effective.Balda.Database.Resolve(stateWorkingDir, stateDir)
	return stateDir, database, err
}
