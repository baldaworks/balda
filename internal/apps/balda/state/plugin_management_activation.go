package state

import (
	"context"
	"database/sql"
	"errors"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	pluginOpInstall     = "install"
	pluginOpUpgrade     = "upgrade"
	pluginOpEnable      = "enable"
	pluginOpDisable     = "disable"
	pluginOpRollback    = "rollback"
	pluginOpRemove      = "remove"
	pluginOpAdoptOrigin = "adopt-origin"
	pluginOriginUnknown = "origin-unknown"
)

func (s *sqlPluginManagementStore) CommitPlugin(ctx context.Context, m PluginCommit) error {
	if m.Authority == nil {
		return plugincmd.ErrInvalid
	}
	return s.commitPlugin(ctx, m)
}

func (s *sqlPluginManagementStore) CommitPluginTrusted(ctx context.Context, m PluginCommit) error {
	if m.Authority != nil {
		return plugincmd.ErrInvalid
	}
	return s.commitPlugin(ctx, m)
}

func (s *sqlPluginManagementStore) commitPlugin(ctx context.Context, m PluginCommit) error {
	if err := validatePluginCommit(m); err != nil {
		return err
	}
	audit := pluginAudit(m.Authority, m.AuditID, pluginAuditAction(m.Intent.Operation), usercmd.AuditTargetPlugin, m.PluginID, m.Intent.UpdatedAt)
	return s.transaction(ctx, m.Authority, audit, func(tx *sql.Tx) error {
		// Marketplace before plugin is shared by every acceptance path.
		if m.MarketplaceName != "" {
			if err := s.lockGeneration(ctx, tx, marketplaceGenerationPrefix+m.MarketplaceName, m.Intent.UpdatedAt); err != nil {
				return err
			}
			market, err := s.observeMarketplace(ctx, tx, m.MarketplaceName)
			if err != nil {
				return err
			}
			if market.DefinitionJSON == nil || market.Token() != m.ExpectedMarketplaceState {
				return plugincmd.ErrConflict
			}
		}
		if err := s.lockGeneration(ctx, tx, pluginGenerationPrefix+m.PluginID, m.Intent.UpdatedAt); err != nil {
			return err
		}
		observed, err := s.observePlugin(ctx, tx, m.PluginID)
		if err != nil {
			return err
		}
		if observed.Token() != m.ExpectedState {
			return plugincmd.ErrConflict
		}
		if err := validatePluginTransition(m, observed); err != nil {
			return err
		}
		if m.Revision != nil {
			if err := s.putRevision(ctx, tx, *m.Revision); err != nil {
				return err
			}
		}
		if m.Install == nil {
			if _, err := tx.ExecContext(ctx, s.users.bind(`DELETE FROM balda_plugin_installs WHERE plugin_id=?`), m.PluginID); err != nil {
				return plugincmd.ErrUnavailable
			}
		} else {
			if err := s.switchInstall(ctx, tx, *m.Install); err != nil {
				return err
			}
		}
		if observed.Install != nil && (m.Install == nil || observed.Install.ActiveRevisionID != m.Install.ActiveRevisionID) {
			if _, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_plugin_revisions SET retired_at=? WHERE plugin_id=? AND revision_id=?`), formatPluginTime(m.Intent.UpdatedAt), m.PluginID, observed.Install.ActiveRevisionID); err != nil {
				return plugincmd.ErrUnavailable
			}
		}
		if err := s.putIntent(ctx, tx, m.Intent); err != nil {
			return err
		}
		return s.advanceGeneration(ctx, tx, pluginGenerationPrefix+m.PluginID, observed.Generation, m.Intent.UpdatedAt)
	})
}

func (s *sqlPluginManagementStore) observePlugin(ctx context.Context, tx *sql.Tx, id string) (PluginObservation, error) {
	result := PluginObservation{PluginID: id}
	var err error
	result.Generation, err = s.readGeneration(ctx, tx, pluginGenerationPrefix+id)
	if err != nil {
		return result, err
	}
	install, err := scanPluginInstall(tx.QueryRowContext(ctx, s.users.bind(pluginInstallSelect+` WHERE plugin_id=?`)+s.users.forUpdate, id))
	switch {
	case err == nil:
		result.Install = &install
	case !errors.Is(err, sql.ErrNoRows):
		return result, plugincmd.ErrUnavailable
	}
	rows, err := tx.QueryContext(ctx, s.users.bind(`SELECT plugin_id,revision_id,version,description,relative_root,capability_json,created_at,retired_at FROM balda_plugin_revisions WHERE plugin_id=? ORDER BY revision_id`)+s.users.forUpdate, id)
	if err != nil {
		return result, plugincmd.ErrUnavailable
	}
	for rows.Next() {
		revision, err := scanPluginRevision(rows)
		if err != nil {
			_ = rows.Close()
			return result, plugincmd.ErrUnavailable
		}
		result.Revisions = append(result.Revisions, revision)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return result, plugincmd.ErrUnavailable
	}
	rows, err = tx.QueryContext(ctx, s.users.bind(`SELECT intent_id,plugin_id,COALESCE(from_revision_id,''),to_revision_id,operation,state,created_at,updated_at FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? ORDER BY intent_id`)+s.users.forUpdate, id, PluginActivationIntentPending)
	if err != nil {
		return result, plugincmd.ErrUnavailable
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		intent, err := scanPluginIntent(rows)
		if err != nil {
			return result, plugincmd.ErrUnavailable
		}
		result.Pending = append(result.Pending, intent)
	}
	if err := rows.Err(); err != nil {
		return result, plugincmd.ErrUnavailable
	}
	return result, nil
}

func (s *sqlPluginManagementStore) putRevision(ctx context.Context, tx *sql.Tx, r PluginRevisionRecord) error {
	_, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_plugin_revisions (plugin_id,revision_id,version,description,relative_root,capability_json,created_at,retired_at) VALUES (?,?,?,?,?,?,?,?) ON CONFLICT(plugin_id,revision_id) DO NOTHING`), r.PluginID, r.RevisionID, r.Version, r.Description, r.RelativeRoot, r.CapabilityJSON, formatPluginTime(r.CreatedAt), formatPluginTime(r.RetiredAt))
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	existing, err := scanPluginRevision(tx.QueryRowContext(ctx, s.users.bind(`SELECT plugin_id,revision_id,version,description,relative_root,capability_json,created_at,retired_at FROM balda_plugin_revisions WHERE plugin_id=? AND revision_id=?`), r.PluginID, r.RevisionID))
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	if !samePluginRevision(existing, r) {
		return plugincmd.ErrConflict
	}
	return nil
}

func (s *sqlPluginManagementStore) switchInstall(ctx context.Context, tx *sql.Tx, r PluginInstallRecord) error {
	target, err := scanPluginRevision(tx.QueryRowContext(ctx, s.users.bind(`SELECT plugin_id,revision_id,version,description,relative_root,capability_json,created_at,retired_at FROM balda_plugin_revisions WHERE plugin_id=? AND revision_id=?`)+s.users.forUpdate, r.PluginID, r.ActiveRevisionID))
	if errors.Is(err, sql.ErrNoRows) {
		return plugincmd.ErrConflict
	}
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	if target.Version != r.Version || target.Description != r.Description || target.CapabilityJSON != r.CapabilityJSON {
		return plugincmd.ErrInvalid
	}

	result, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_plugin_revisions SET retired_at='' WHERE plugin_id=? AND revision_id=?`), r.PluginID, r.ActiveRevisionID)
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	n, err := result.RowsAffected()
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	if n != 1 {
		return plugincmd.ErrConflict
	}
	_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_plugin_installs (plugin_id,origin_marketplace,origin_source,origin_path,active_revision_id,enabled,version,description,capability_json,data_relative_path,updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(plugin_id) DO UPDATE SET origin_marketplace=excluded.origin_marketplace,origin_source=excluded.origin_source,origin_path=excluded.origin_path,active_revision_id=excluded.active_revision_id,enabled=excluded.enabled,version=excluded.version,description=excluded.description,capability_json=excluded.capability_json,updated_at=excluded.updated_at`), r.PluginID, r.OriginMarketplace, r.OriginSource, r.OriginPath, r.ActiveRevisionID, postgresBool(r.Enabled), r.Version, r.Description, r.CapabilityJSON, r.DataRelativePath, formatPluginTime(r.UpdatedAt))
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func (s *sqlPluginManagementStore) putIntent(ctx context.Context, tx *sql.Tx, r PluginActivationIntent) error {
	_, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_plugin_activation_intents (intent_id,plugin_id,from_revision_id,to_revision_id,operation,state,created_at,updated_at) VALUES (?,?,NULLIF(?,''),?,?,?,?,?)`), r.IntentID, r.PluginID, r.FromRevisionID, r.ToRevisionID, r.Operation, r.State, formatPluginTime(r.CreatedAt), formatPluginTime(r.UpdatedAt))
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func validatePluginCommit(m PluginCommit) error {
	if !normalizedID(m.PluginID) || m.PluginID != m.Intent.PluginID || m.ExpectedState == "" || !normalizedID(m.AuditID) {
		return plugincmd.ErrInvalid
	}
	if (m.MarketplaceName == "") != (m.ExpectedMarketplaceState == "") {
		return plugincmd.ErrInvalid
	}
	if m.MarketplaceName != "" && !normalizedID(m.MarketplaceName) {
		return plugincmd.ErrInvalid
	}
	if m.Revision != nil && (m.Install == nil || m.Revision.RevisionID != m.Install.ActiveRevisionID || m.Revision.PluginID != m.PluginID || validatePluginRevision(*m.Revision) != nil) {
		return plugincmd.ErrInvalid
	}
	if m.Install == nil {
		r := m.Intent
		if r.Operation != pluginOpRemove || m.Revision != nil || !normalizedID(r.IntentID) || !normalizedID(r.FromRevisionID) || r.ToRevisionID != r.FromRevisionID || r.State != PluginActivationIntentPending || r.CreatedAt.IsZero() || r.UpdatedAt.IsZero() {
			return plugincmd.ErrInvalid
		}
	} else if validateActivation(m.Intent, *m.Install) != nil {
		return plugincmd.ErrInvalid
	}
	switch m.Intent.Operation {
	case pluginOpAdoptOrigin:
		if m.Authority != nil || m.Intent.FromRevisionID != m.Intent.ToRevisionID {
			return plugincmd.ErrInvalid
		}
		return nil
	case pluginOpInstall, pluginOpUpgrade, pluginOpEnable, pluginOpDisable, pluginOpRollback, pluginOpRemove:
		return nil
	default:
		return plugincmd.ErrInvalid
	}
}

func validatePluginTransition(m PluginCommit, o PluginObservation) error {
	if o.Install == nil {
		if m.Intent.Operation != pluginOpInstall || m.Install == nil || m.Intent.FromRevisionID != "" {
			return plugincmd.ErrConflict
		}
		return nil
	}
	if m.Intent.Operation == pluginOpInstall || m.Intent.FromRevisionID != o.Install.ActiveRevisionID {
		return plugincmd.ErrConflict
	}
	if m.Install == nil {
		return nil
	}
	old, next := o.Install, m.Install
	if m.Intent.Operation == pluginOpAdoptOrigin {
		if old.OriginMarketplace != pluginOriginUnknown || old.OriginSource != pluginOriginUnknown || old.OriginPath != pluginOriginUnknown || old.DataRelativePath != next.DataRelativePath || old.Enabled != next.Enabled || old.ActiveRevisionID != next.ActiveRevisionID || old.Version != next.Version || old.Description != next.Description || old.CapabilityJSON != next.CapabilityJSON {
			return plugincmd.ErrConflict
		}
		return nil
	}
	if old.OriginMarketplace != next.OriginMarketplace || old.OriginSource != next.OriginSource || old.OriginPath != next.OriginPath || old.DataRelativePath != next.DataRelativePath {
		return plugincmd.ErrConflict
	}
	switch m.Intent.Operation {
	case pluginOpEnable, pluginOpDisable:
		if old.ActiveRevisionID != next.ActiveRevisionID || old.Version != next.Version || old.Description != next.Description || old.CapabilityJSON != next.CapabilityJSON || next.Enabled != (m.Intent.Operation == pluginOpEnable) || old.Enabled == next.Enabled {
			return plugincmd.ErrConflict
		}
	case pluginOpUpgrade, pluginOpRollback:
		if old.Enabled != next.Enabled {
			return plugincmd.ErrConflict
		}
	}
	return nil
}

func pluginAuditAction(operation string) usercmd.AuditAction {
	switch operation {
	case pluginOpAdoptOrigin:
		return usercmd.AuditActionPluginOriginAdopted
	case pluginOpInstall:
		return usercmd.AuditActionPluginInstalled
	case pluginOpUpgrade:
		return usercmd.AuditActionPluginUpgraded
	case pluginOpEnable:
		return usercmd.AuditActionPluginEnabled
	case pluginOpDisable:
		return usercmd.AuditActionPluginDisabled
	case pluginOpRollback:
		return usercmd.AuditActionPluginRolledBack
	default:
		return usercmd.AuditActionPluginRemoved
	}
}
