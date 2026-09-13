package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type sqlitePluginStore struct{ db *sql.DB }

func (s *sqlitePluginStore) PutPluginRevision(ctx context.Context, record PluginRevisionRecord) error {
	if err := validatePluginRevision(record); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO balda_plugin_revisions
		(plugin_id, revision_id, version, relative_root, capability_json, created_at, retired_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id, revision_id) DO NOTHING`, record.PluginID, record.RevisionID, record.Version,
		record.RelativeRoot, record.CapabilityJSON, formatPluginTime(record.CreatedAt), formatPluginTime(record.RetiredAt))
	if err != nil {
		return fmt.Errorf("put plugin revision: %w", err)
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

func (s *sqlitePluginStore) GetPluginRevision(ctx context.Context, pluginID, revisionID string) (PluginRevisionRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT plugin_id, revision_id, version, relative_root, capability_json, created_at, retired_at
		FROM balda_plugin_revisions WHERE plugin_id = ? AND revision_id = ?`, pluginID, revisionID)
	record, err := scanPluginRevision(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginRevisionRecord{}, false, nil
	}
	if err != nil {
		return PluginRevisionRecord{}, false, fmt.Errorf("get plugin revision: %w", err)
	}
	return record, true, nil
}

func (s *sqlitePluginStore) ListPluginRevisions(ctx context.Context, pluginID string) ([]PluginRevisionRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT plugin_id, revision_id, version, relative_root, capability_json, created_at, retired_at
		FROM balda_plugin_revisions WHERE plugin_id = ? ORDER BY created_at, revision_id`, pluginID)
	if err != nil {
		return nil, fmt.Errorf("list plugin revisions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginRevisionRecord
	for rows.Next() {
		record, scanErr := scanPluginRevision(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin revisions: %w", err)
	}
	return records, nil
}

func (s *sqlitePluginStore) GetPluginInstall(ctx context.Context, pluginID string) (PluginInstallRecord, bool, error) {
	row := s.db.QueryRowContext(ctx, pluginInstallSelect+` WHERE plugin_id = ?`, pluginID)
	record, err := scanPluginInstall(row)
	if errors.Is(err, sql.ErrNoRows) {
		return PluginInstallRecord{}, false, nil
	}
	if err != nil {
		return PluginInstallRecord{}, false, fmt.Errorf("get plugin install: %w", err)
	}
	return record, true, nil
}

func (s *sqlitePluginStore) ListPluginInstalls(ctx context.Context) ([]PluginInstallRecord, error) {
	rows, err := s.db.QueryContext(ctx, pluginInstallSelect+` ORDER BY plugin_id`)
	if err != nil {
		return nil, fmt.Errorf("list plugin installs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginInstallRecord
	for rows.Next() {
		record, scanErr := scanPluginInstall(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin installs: %w", err)
	}
	return records, nil
}

func (s *sqlitePluginStore) ActivatePlugin(ctx context.Context, intent PluginActivationIntent, install PluginInstallRecord) error {
	if err := validateActivation(intent, install); err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin activation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var marketplace, source, path, active, dataPath string
	err = tx.QueryRowContext(ctx, `SELECT origin_marketplace, origin_source, origin_path, active_revision_id, data_relative_path
		FROM balda_plugin_installs WHERE plugin_id = ?`, install.PluginID).Scan(&marketplace, &source, &path, &active, &dataPath)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		if intent.FromRevisionID != "" {
			return errors.New("initial activation cannot have a from revision")
		}
	case err != nil:
		return fmt.Errorf("read current plugin activation: %w", err)
	default:
		if marketplace != install.OriginMarketplace || source != install.OriginSource || path != install.OriginPath || dataPath != install.DataRelativePath {
			return errors.New("plugin origin is locked")
		}
		if intent.FromRevisionID != active {
			return errors.New("activation from revision does not match active revision")
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO balda_plugin_activation_intents
		(intent_id, plugin_id, from_revision_id, to_revision_id, operation, state, created_at, updated_at)
		VALUES (?, ?, NULLIF(?, ''), ?, ?, ?, ?, ?)`, intent.IntentID, intent.PluginID, intent.FromRevisionID,
		intent.ToRevisionID, intent.Operation, PluginActivationIntentPending, formatPluginTime(intent.CreatedAt), formatPluginTime(intent.UpdatedAt)); err != nil {
		return fmt.Errorf("insert plugin activation intent: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO balda_plugin_installs
		(plugin_id, origin_marketplace, origin_source, origin_path, active_revision_id, enabled, version, capability_json, data_relative_path, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(plugin_id) DO UPDATE SET active_revision_id=excluded.active_revision_id, enabled=excluded.enabled,
		version=excluded.version, capability_json=excluded.capability_json, data_relative_path=excluded.data_relative_path, updated_at=excluded.updated_at`,
		install.PluginID, install.OriginMarketplace, install.OriginSource, install.OriginPath, install.ActiveRevisionID,
		install.Enabled, install.Version, install.CapabilityJSON, install.DataRelativePath, formatPluginTime(install.UpdatedAt)); err != nil {
		return fmt.Errorf("switch active plugin revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin activation: %w", err)
	}
	return nil
}

func (s *sqlitePluginStore) SetPluginEnabled(ctx context.Context, pluginID string, enabled bool, updatedAt time.Time) error {
	if !normalizedID(pluginID) || updatedAt.IsZero() {
		return errors.New("plugin ID and update time are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE balda_plugin_installs SET enabled=?, updated_at=? WHERE plugin_id=?`, enabled, formatPluginTime(updatedAt), pluginID)
	return requireAffected(result, err, "set plugin enabled")
}

func (s *sqlitePluginStore) CompletePluginActivation(ctx context.Context, intentID string, updatedAt time.Time) error {
	if !normalizedID(intentID) || updatedAt.IsZero() {
		return errors.New("intent ID and update time are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE balda_plugin_activation_intents SET state=?, updated_at=? WHERE intent_id=? AND state=?`,
		PluginActivationIntentComplete, formatPluginTime(updatedAt), intentID, PluginActivationIntentPending)
	return requireAffected(result, err, "complete plugin activation")
}

func (s *sqlitePluginStore) ListIncompletePluginActivations(ctx context.Context) ([]PluginActivationIntent, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT intent_id, plugin_id, COALESCE(from_revision_id,''), to_revision_id, operation, state, created_at, updated_at
		FROM balda_plugin_activation_intents WHERE state=? ORDER BY created_at, intent_id`, PluginActivationIntentPending)
	if err != nil {
		return nil, fmt.Errorf("list incomplete plugin activations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var records []PluginActivationIntent
	for rows.Next() {
		record, scanErr := scanPluginIntent(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate plugin activations: %w", err)
	}
	return records, nil
}

func (s *sqlitePluginStore) RetirePluginRevision(ctx context.Context, pluginID, revisionID string, retiredAt time.Time) error {
	if !normalizedID(pluginID) || !normalizedID(revisionID) || retiredAt.IsZero() {
		return errors.New("plugin ID, revision ID, and retirement time are required")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE balda_plugin_revisions SET retired_at=? WHERE plugin_id=? AND revision_id=?`, formatPluginTime(retiredAt), pluginID, revisionID)
	return requireAffected(result, err, "retire plugin revision")
}

func (s *sqlitePluginStore) CanPurgePluginRevision(ctx context.Context, pluginID, revisionID string) (bool, error) {
	if !normalizedID(pluginID) || !normalizedID(revisionID) {
		return false, errors.New("plugin ID and revision ID are required")
	}
	var references int
	err := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?) +
		(SELECT COUNT(*) FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? AND (from_revision_id=? OR to_revision_id=?))`,
		pluginID, revisionID, pluginID, PluginActivationIntentPending, revisionID, revisionID).Scan(&references)
	if err != nil {
		return false, fmt.Errorf("check plugin revision references: %w", err)
	}
	return references == 0, nil
}

func (s *sqlitePluginStore) PurgePluginRevision(ctx context.Context, pluginID, revisionID string) error {
	if !normalizedID(pluginID) || !normalizedID(revisionID) {
		return errors.New("plugin ID and revision ID are required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin plugin revision purge: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `DELETE FROM balda_plugin_revisions
		WHERE plugin_id=? AND revision_id=?
		AND NOT EXISTS (SELECT 1 FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?)
		AND NOT EXISTS (SELECT 1 FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? AND (from_revision_id=? OR to_revision_id=?))`,
		pluginID, revisionID, pluginID, revisionID, pluginID, PluginActivationIntentPending, revisionID, revisionID)
	if err := requireAffected(result, err, "purge plugin revision"); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit plugin revision purge: %w", err)
	}
	return nil
}

const pluginInstallSelect = `SELECT plugin_id, origin_marketplace, origin_source, origin_path, active_revision_id,
	enabled, version, capability_json, data_relative_path, updated_at FROM balda_plugin_installs`

type rowScanner interface{ Scan(dest ...any) error }

func scanPluginRevision(row rowScanner) (PluginRevisionRecord, error) {
	var record PluginRevisionRecord
	var created, retired string
	if err := row.Scan(&record.PluginID, &record.RevisionID, &record.Version, &record.RelativeRoot, &record.CapabilityJSON, &created, &retired); err != nil {
		return record, err
	}
	var err error
	record.CreatedAt, err = parsePluginTime(created)
	if err != nil {
		return record, err
	}
	record.RetiredAt, err = parsePluginTime(retired)
	return record, err
}

func scanPluginInstall(row rowScanner) (PluginInstallRecord, error) {
	var record PluginInstallRecord
	var enabled int
	var updated string
	if err := row.Scan(&record.PluginID, &record.OriginMarketplace, &record.OriginSource, &record.OriginPath, &record.ActiveRevisionID, &enabled, &record.Version, &record.CapabilityJSON, &record.DataRelativePath, &updated); err != nil {
		return record, err
	}
	record.Enabled = enabled != 0
	var err error
	record.UpdatedAt, err = parsePluginTime(updated)
	return record, err
}

func scanPluginIntent(row rowScanner) (PluginActivationIntent, error) {
	var record PluginActivationIntent
	var created, updated string
	if err := row.Scan(&record.IntentID, &record.PluginID, &record.FromRevisionID, &record.ToRevisionID, &record.Operation, &record.State, &created, &updated); err != nil {
		return record, err
	}
	var err error
	record.CreatedAt, err = parsePluginTime(created)
	if err != nil {
		return record, err
	}
	record.UpdatedAt, err = parsePluginTime(updated)
	return record, err
}

func validatePluginRevision(record PluginRevisionRecord) error {
	if !normalizedID(record.PluginID) || !normalizedID(record.RevisionID) || !validStoredRelativePath(record.RelativeRoot) || !json.Valid([]byte(record.CapabilityJSON)) || record.CreatedAt.IsZero() {
		return errors.New("invalid plugin revision")
	}
	return nil
}

func validateActivation(intent PluginActivationIntent, install PluginInstallRecord) error {
	if !normalizedID(intent.IntentID) || !normalizedID(intent.PluginID) || intent.PluginID != install.PluginID || !normalizedID(intent.ToRevisionID) || (intent.FromRevisionID != "" && !normalizedID(intent.FromRevisionID)) || intent.ToRevisionID != install.ActiveRevisionID || intent.State != PluginActivationIntentPending || strings.TrimSpace(intent.Operation) == "" || intent.Operation != strings.TrimSpace(intent.Operation) || intent.CreatedAt.IsZero() || intent.UpdatedAt.IsZero() || install.OriginSource == "" || install.OriginPath == "" || !validStoredRelativePath(install.DataRelativePath) || !json.Valid([]byte(install.CapabilityJSON)) || install.UpdatedAt.IsZero() {
		return errors.New("invalid plugin activation")
	}
	return nil
}

func normalizedID(value string) bool { return value != "" && value == strings.TrimSpace(value) }

func samePluginRevision(left, right PluginRevisionRecord) bool {
	return left.PluginID == right.PluginID && left.RevisionID == right.RevisionID && left.Version == right.Version &&
		left.RelativeRoot == right.RelativeRoot && left.CapabilityJSON == right.CapabilityJSON &&
		left.CreatedAt.Equal(right.CreatedAt) && left.RetiredAt.Equal(right.RetiredAt)
}

func validStoredRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.VolumeName(value) != "" {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(value))
	return value == clean && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../")
}

func requireAffected(result sql.Result, err error, operation string) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}
	if affected != 1 {
		return fmt.Errorf("%s: record not found", operation)
	}
	return nil
}

func formatPluginTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
func parsePluginTime(value string) (time.Time, error) {
	if value == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, value)
}
