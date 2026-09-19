package state

import (
	"context"
	"database/sql"

	"errors"

	"time"
)

type postgresPluginStore struct{ db *sql.DB }

func (s *postgresPluginStore) PutPluginRevision(ctx context.Context, record PluginRevisionRecord) error {
	if err := validatePluginRevision(record); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, postgresBind(`INSERT INTO balda_plugin_revisions
		(plugin_id, revision_id, version, description, relative_root, capability_json, created_at, retired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id, revision_id) DO NOTHING`), record.PluginID, record.RevisionID, record.Version,
		record.Description, record.RelativeRoot, record.CapabilityJSON, formatPluginTime(record.CreatedAt), formatPluginTime(record.RetiredAt))
	if err != nil {
		return postgresErrorf("put plugin revision: %w", err)
	}
	stored, found, err := s.GetPluginRevision(ctx, record.PluginID, record.RevisionID)
	if err != nil {
		return err
	}
	if !found || !samePluginRevision(stored, record) {
		return errors.New("plugin revision conflicts with immutable record")
	}
	return nil
}

func (s *postgresPluginStore) GetPluginRevision(ctx context.Context, pluginID, revisionID string) (PluginRevisionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(`SELECT plugin_id, revision_id, version, description, relative_root, capability_json, created_at, retired_at
		FROM balda_plugin_revisions WHERE plugin_id = ? AND revision_id = ?`), pluginID, revisionID)
	record, err := scanPluginRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginRevisionRecord{}, false, nil
	}
	if err != nil {
		return PluginRevisionRecord{}, false, postgresErrorf("get plugin revision: %w", err)
	}
	return record, true, nil
}

func (s *postgresPluginStore) ListPluginRevisions(ctx context.Context, pluginID string) ([]PluginRevisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, postgresBind(`SELECT plugin_id, revision_id, version, description, relative_root, capability_json, created_at, retired_at
		FROM balda_plugin_revisions WHERE plugin_id = ? ORDER BY created_at, revision_id`), pluginID)
	if err != nil {
		return nil, postgresErrorf("list plugin revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginRevisionRecord
	for rows.Next() {
		record, scanErr := scanPluginRevision(rows)
		if scanErr != nil {
			return nil, redactPostgresError(scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate plugin revisions: %w", err)
	}
	return records, nil
}

func (s *postgresPluginStore) GetPluginInstall(ctx context.Context, pluginID string) (PluginInstallRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, postgresBind(pluginInstallSelect+` WHERE plugin_id = ?`), pluginID)
	record, err := scanPluginInstall(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginInstallRecord{}, false, nil
	}
	if err != nil {
		return PluginInstallRecord{}, false, postgresErrorf("get plugin install: %w", err)
	}
	return record, true, nil
}

func (s *postgresPluginStore) ListPluginInstalls(ctx context.Context) ([]PluginInstallRecord, error) {
	rows, err := s.db.QueryContext(ctx, postgresBind(pluginInstallSelect+` ORDER BY plugin_id`))
	if err != nil {
		return nil, postgresErrorf("list plugin installs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginInstallRecord
	for rows.Next() {
		record, scanErr := scanPluginInstall(rows)
		if scanErr != nil {
			return nil, redactPostgresError(scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate plugin installs: %w", err)
	}
	return records, nil
}

func (s *postgresPluginStore) ActivatePlugin(ctx context.Context, intent PluginActivationIntent, install PluginInstallRecord) error {
	if err := validateActivation(intent, install); err != nil {
		return err
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin plugin activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var marketplace, source, path, active, dataPath string
	err = tx.QueryRowContext(ctx, postgresBind(`SELECT origin_marketplace, origin_source, origin_path, active_revision_id, data_relative_path
		FROM balda_plugin_installs WHERE plugin_id = ?`), install.PluginID).Scan(&marketplace, &source, &path, &active, &dataPath)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if intent.FromRevisionID != "" {
			return errors.New("initial activation cannot have a from revision")
		}
	case err != nil:
		return postgresErrorf("read current plugin activation: %w", err)
	default:
		if marketplace != install.OriginMarketplace || source != install.OriginSource || path != install.OriginPath || dataPath != install.DataRelativePath {
			return errors.New("plugin origin is locked")
		}
		if intent.FromRevisionID != active {
			return errors.New("activation from revision does not match active revision")
		}
	}
	result, err := tx.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_revisions SET retired_at=''
		WHERE plugin_id=? AND revision_id=?`), install.PluginID, install.ActiveRevisionID)
	if err := requireAffected(result, redactPostgresError(err), "activate plugin revision"); err != nil {
		return err
	}
	if intent.FromRevisionID != "" && intent.FromRevisionID != intent.ToRevisionID {
		result, err = tx.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_revisions SET retired_at=?
			WHERE plugin_id=? AND revision_id=?`), formatPluginTime(install.UpdatedAt), install.PluginID, intent.FromRevisionID)
		if err := requireAffected(result, redactPostgresError(err), "retire previous plugin revision"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`INSERT INTO balda_plugin_activation_intents
		(intent_id, plugin_id, from_revision_id, to_revision_id, operation, state, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`), intent.IntentID, intent.PluginID, intent.FromRevisionID,
		intent.ToRevisionID, intent.Operation, PluginActivationIntentPending, formatPluginTime(intent.CreatedAt), formatPluginTime(intent.UpdatedAt)); err != nil {
		return postgresErrorf("insert plugin activation intent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`INSERT INTO balda_plugin_installs
		(plugin_id, origin_marketplace, origin_source, origin_path, active_revision_id, enabled, version, description, capability_json, data_relative_path, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id) DO UPDATE SET active_revision_id=excluded.active_revision_id, enabled=excluded.enabled,
		version=excluded.version, description=excluded.description, capability_json=excluded.capability_json, data_relative_path=excluded.data_relative_path, updated_at=excluded.updated_at`), install.PluginID, install.OriginMarketplace, install.OriginSource, install.OriginPath, install.ActiveRevisionID,
		postgresBool(install.Enabled), install.Version, install.Description, install.CapabilityJSON, install.DataRelativePath, formatPluginTime(install.UpdatedAt)); err != nil {
		return postgresErrorf("switch active plugin revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit plugin activation: %w", err)
	}
	return nil
}

func (s *postgresPluginStore) AdoptPluginOrigin(ctx context.Context, intent PluginActivationIntent, install PluginInstallRecord) error {
	if err := validateActivation(intent, install); err != nil || intent.Operation != "adopt-origin" || intent.FromRevisionID != intent.ToRevisionID {
		return errors.New("invalid plugin origin adoption")
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin plugin origin adoption: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_installs
		SET origin_marketplace=?, origin_source=?, origin_path=?, updated_at=?
		WHERE plugin_id=? AND active_revision_id=? AND origin_marketplace='origin-unknown'
		AND origin_source='origin-unknown' AND origin_path='origin-unknown' AND data_relative_path=?`), install.OriginMarketplace, install.OriginSource, install.OriginPath, formatPluginTime(install.UpdatedAt),
		install.PluginID, install.ActiveRevisionID, install.DataRelativePath)
	if err := requireAffected(result, redactPostgresError(err), "adopt plugin origin"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`INSERT INTO balda_plugin_activation_intents
		(intent_id, plugin_id, from_revision_id, to_revision_id, operation, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), intent.IntentID, intent.PluginID, intent.FromRevisionID, intent.ToRevisionID,
		intent.Operation, intent.State, formatPluginTime(intent.CreatedAt), formatPluginTime(intent.UpdatedAt)); err != nil {
		return postgresErrorf("insert plugin origin adoption intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit plugin origin adoption: %w", err)
	}
	return nil
}

func (s *postgresPluginStore) DeactivatePlugin(ctx context.Context, intent PluginActivationIntent) error {
	if !normalizedID(intent.IntentID) || !normalizedID(intent.PluginID) || !normalizedID(intent.FromRevisionID) || intent.ToRevisionID != intent.FromRevisionID || intent.State != PluginActivationIntentPending || intent.Operation != "remove" || intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() {
		return errors.New("invalid plugin deactivation")
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin plugin deactivation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, postgresBind(`DELETE FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?`), intent.PluginID, intent.FromRevisionID)
	if err := requireAffected(result, redactPostgresError(err), "remove plugin install"); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`INSERT INTO balda_plugin_activation_intents
		(intent_id, plugin_id, from_revision_id, to_revision_id, operation, state, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`), intent.IntentID, intent.PluginID, intent.FromRevisionID, intent.ToRevisionID,
		intent.Operation, intent.State, formatPluginTime(intent.CreatedAt), formatPluginTime(intent.UpdatedAt)); err != nil {
		return postgresErrorf("insert plugin deactivation intent: %w", err)
	}
	result, err = tx.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_revisions SET retired_at=?
		WHERE plugin_id=? AND revision_id=? AND retired_at=''`), formatPluginTime(intent.UpdatedAt), intent.PluginID, intent.FromRevisionID)
	if err := requireAffected(result, redactPostgresError(err), "retire removed plugin revision"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit plugin deactivation: %w", err)
	}
	return nil
}

func (s *postgresPluginStore) SetPluginEnabled(ctx context.Context, pluginID string, enabled bool, updatedAt time.Time) error {
	if !normalizedID(pluginID) || updatedAt.IsZero() {
		return errors.New("plugin ID and update time are required")
	}
	result, err := s.db.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_installs SET enabled=?, updated_at=? WHERE plugin_id=?`), postgresBool(enabled), formatPluginTime(updatedAt), pluginID)
	return requireAffected(result, redactPostgresError(err), "set plugin enabled")
}

func (s *postgresPluginStore) CompletePluginActivation(ctx context.Context, intentID string, updatedAt time.Time) error {
	if !normalizedID(intentID) || updatedAt.IsZero() {
		return errors.New("intent ID and update time are required")
	}
	result, err := s.db.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_activation_intents SET state=?, updated_at=? WHERE intent_id=? AND state=?`), PluginActivationIntentComplete, formatPluginTime(updatedAt), intentID, PluginActivationIntentPending)
	return requireAffected(result, redactPostgresError(err), "complete plugin activation")
}

func (s *postgresPluginStore) ListIncompletePluginActivations(ctx context.Context) ([]PluginActivationIntent, error) {
	rows, err := s.db.QueryContext(ctx, postgresBind(`SELECT intent_id, plugin_id, COALESCE(from_revision_id,''), to_revision_id, operation, state, created_at, updated_at
		FROM balda_plugin_activation_intents WHERE state=? ORDER BY created_at, intent_id`), PluginActivationIntentPending)
	if err != nil {
		return nil, postgresErrorf("list incomplete plugin activations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginActivationIntent
	for rows.Next() {
		record, scanErr := scanPluginIntent(rows)
		if scanErr != nil {
			return nil, redactPostgresError(scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate plugin activations: %w", err)
	}
	return records, nil
}

func (s *postgresPluginStore) RetirePluginRevision(ctx context.Context, pluginID, revisionID string, retiredAt time.Time) error {
	if !normalizedID(pluginID) || !normalizedID(revisionID) || retiredAt.IsZero() {
		return errors.New("plugin ID, revision ID, and retirement time are required")
	}
	result, err := s.db.ExecContext(ctx, postgresBind(`UPDATE balda_plugin_revisions SET retired_at=? WHERE plugin_id=? AND revision_id=?`), formatPluginTime(retiredAt), pluginID, revisionID)
	return requireAffected(result, redactPostgresError(err), "retire plugin revision")
}

func (s *postgresPluginStore) CanPurgePluginRevision(ctx context.Context, pluginID, revisionID string) (bool, error) {
	if !normalizedID(pluginID) || !normalizedID(revisionID) {
		return false, errors.New("plugin ID and revision ID are required")
	}
	var references int
	err := s.db.QueryRowContext(ctx, postgresBind(`SELECT
		(SELECT COUNT(*) FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?) +
		(SELECT COUNT(*) FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? AND (from_revision_id=? OR to_revision_id=?))`), pluginID, revisionID, pluginID, PluginActivationIntentPending, revisionID, revisionID).Scan(&references)
	if err != nil {
		return false, postgresErrorf("check plugin revision references: %w", err)
	}
	return references == 0, nil
}

func (s *postgresPluginStore) PurgePluginRevision(ctx context.Context, pluginID, revisionID string) error {
	if !normalizedID(pluginID) || !normalizedID(revisionID) {
		return errors.New("plugin ID and revision ID are required")
	}
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin plugin revision purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, postgresBind(`DELETE FROM balda_plugin_revisions
		WHERE plugin_id=? AND revision_id=?
		AND NOT EXISTS (SELECT 1 FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?)
		AND NOT EXISTS (SELECT 1 FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? AND (from_revision_id=? OR to_revision_id=?))`), pluginID, revisionID, pluginID, revisionID, pluginID, PluginActivationIntentPending, revisionID, revisionID)
	if err := requireAffected(result, redactPostgresError(err), "purge plugin revision"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit plugin revision purge: %w", err)
	}
	return nil
}
