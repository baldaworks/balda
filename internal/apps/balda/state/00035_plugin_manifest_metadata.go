package state

import (
	"context"
	"database/sql"
	"fmt"
)

func up00035PluginManifestMetadata(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE balda_plugin_revisions ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE balda_plugin_installs ADD COLUMN description TEXT NOT NULL DEFAULT ''`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("add plugin manifest metadata: %w", err)
		}
	}
	return nil
}

func down00035PluginManifestMetadata(ctx context.Context, tx *sql.Tx) error {
	for _, statement := range []string{
		`ALTER TABLE balda_plugin_installs DROP COLUMN description`,
		`ALTER TABLE balda_plugin_revisions DROP COLUMN description`,
	} {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("remove plugin manifest metadata: %w", err)
		}
	}
	return nil
}
