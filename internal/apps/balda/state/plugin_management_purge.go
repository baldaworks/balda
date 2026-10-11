package state

import (
	"context"
	"database/sql"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// PurgePluginRevisionTrusted accepts an existing owner purge and its observation
// change atomically. Filesystem staging and restoration remain lifecycle-owned.
func (s *sqlPluginManagementStore) PurgePluginRevisionTrusted(ctx context.Context, pluginID, revisionID, expectedState, auditID string, at time.Time) error {
	if !normalizedID(pluginID) || !normalizedID(revisionID) || expectedState == "" || !normalizedID(auditID) || at.IsZero() {
		return plugincmd.ErrInvalid
	}
	audit := pluginAudit(nil, auditID, usercmd.AuditActionPluginRevisionPurged, usercmd.AuditTargetPlugin, pluginID, at)
	return s.transaction(ctx, nil, audit, func(tx *sql.Tx) error {
		if err := s.lockGeneration(ctx, tx, pluginGenerationPrefix+pluginID, at); err != nil {
			return err
		}
		observed, err := s.observePlugin(ctx, tx, pluginID)
		if err != nil {
			return err
		}
		if observed.Token() != expectedState {
			return plugincmd.ErrConflict
		}
		result, err := tx.ExecContext(ctx, s.users.bind(`DELETE FROM balda_plugin_revisions
   WHERE plugin_id=? AND revision_id=? AND retired_at<>''
   AND NOT EXISTS (SELECT 1 FROM balda_plugin_installs WHERE plugin_id=? AND active_revision_id=?)
   AND NOT EXISTS (SELECT 1 FROM balda_plugin_activation_intents WHERE plugin_id=? AND state=? AND (from_revision_id=? OR to_revision_id=?))`), pluginID, revisionID, pluginID, revisionID, pluginID, PluginActivationIntentPending, revisionID, revisionID)
		if err != nil {
			return plugincmd.ErrUnavailable
		}
		count, err := result.RowsAffected()
		if err != nil {
			return plugincmd.ErrUnavailable
		}
		if count != 1 {
			return plugincmd.ErrConflict
		}
		return s.advanceGeneration(ctx, tx, pluginGenerationPrefix+pluginID, observed.Generation, at)
	})
}
