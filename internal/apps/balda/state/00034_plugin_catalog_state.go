package state

import (
	"context"
	"database/sql"
	"fmt"
)

func up00034PluginCatalogState(ctx context.Context, tx *sql.Tx) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS balda_plugin_revisions (
			plugin_id TEXT NOT NULL,
			revision_id TEXT NOT NULL,
			version TEXT NOT NULL DEFAULT '',
			relative_root TEXT NOT NULL,
			capability_json TEXT NOT NULL,
			created_at TEXT NOT NULL,
			retired_at TEXT NOT NULL DEFAULT '',
			PRIMARY KEY (plugin_id, revision_id)
		)`,
		`CREATE TABLE IF NOT EXISTS balda_plugin_installs (
			plugin_id TEXT PRIMARY KEY,
			origin_marketplace TEXT NOT NULL,
			origin_source TEXT NOT NULL,
			origin_path TEXT NOT NULL,
			active_revision_id TEXT NOT NULL,
			enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
			version TEXT NOT NULL DEFAULT '',
			capability_json TEXT NOT NULL,
			data_relative_path TEXT NOT NULL,
			updated_at TEXT NOT NULL,
			FOREIGN KEY (plugin_id, active_revision_id)
				REFERENCES balda_plugin_revisions(plugin_id, revision_id) ON DELETE RESTRICT
		)`,
		`CREATE TABLE IF NOT EXISTS balda_plugin_activation_intents (
			intent_id TEXT PRIMARY KEY,
			plugin_id TEXT NOT NULL,
			from_revision_id TEXT,
			to_revision_id TEXT NOT NULL,
			operation TEXT NOT NULL,
			state TEXT NOT NULL CHECK (state IN ('pending', 'complete')),
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS idx_balda_plugin_intents_state_created
			ON balda_plugin_activation_intents(state, created_at)`,
	}
	for _, statement := range statements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("create plugin catalog state: %w", err)
		}
	}
	return nil
}

func down00034PluginCatalogState(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`DROP INDEX IF EXISTS idx_balda_plugin_intents_state_created`,
		`DROP TABLE IF EXISTS balda_plugin_activation_intents`,
		`DROP TABLE IF EXISTS balda_plugin_installs`,
		`DROP TABLE IF EXISTS balda_plugin_revisions`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("drop plugin catalog state: %w", err)
		}
	}
	return nil
}
