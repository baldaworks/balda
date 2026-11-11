package state

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/plugincmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

const (
	marketplaceOpAdd           = "add"
	marketplaceOpRefresh       = "refresh"
	marketplaceOpRefreshFailed = "refresh-failed"
	marketplaceOpUpsert        = "upsert"
)

const pluginGenerationPrefix = "plugin_management_generation:"
const marketplaceGenerationPrefix = "plugin_marketplace_generation:"
const marketplaceDefinitionPrefix = "plugin_marketplace:"
const marketplaceMetadataPrefix = "plugin_marketplace_discovery:"

type sqlPluginManagementStore struct{ users *sqlUserStore }

var _ PluginManagementStore = (*sqlPluginManagementStore)(nil)

func (s *sqlPluginManagementStore) CheckAuthority(ctx context.Context, a plugincmd.Authority) error {
	if !validPluginAuthority(a) {
		return plugincmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	locked, err := s.users.checkBrowserAuthority(ctx, tx, pluginBrowserAuthority(a))
	if err != nil {
		return pluginAuthorityError(err)
	}
	if err := locked.checkTime(pluginBrowserAuthority(a)); err != nil {
		return pluginAuthorityError(err)
	}
	if err := tx.Commit(); err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func (s *sqlPluginManagementStore) ObservePlugin(ctx context.Context, id string) (PluginObservation, error) {
	if !normalizedID(id) {
		return PluginObservation{}, plugincmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return PluginObservation{}, plugincmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	result, err := s.observePlugin(ctx, tx, id)
	if err != nil {
		return PluginObservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return PluginObservation{}, plugincmd.ErrUnavailable
	}
	return result, nil
}

func (s *sqlPluginManagementStore) ObserveMarketplace(ctx context.Context, name string) (MarketplaceObservation, error) {
	if !normalizedID(name) {
		return MarketplaceObservation{}, plugincmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return MarketplaceObservation{}, plugincmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	result, err := s.observeMarketplace(ctx, tx, name)
	if err != nil {
		return MarketplaceObservation{}, err
	}
	if err := tx.Commit(); err != nil {
		return MarketplaceObservation{}, plugincmd.ErrUnavailable
	}
	return result, nil
}

func (s *sqlPluginManagementStore) CommitMarketplace(ctx context.Context, m MarketplaceCommit) error {
	if m.Authority == nil {
		return plugincmd.ErrInvalid
	}
	return s.commitMarketplace(ctx, m)
}

func (s *sqlPluginManagementStore) CommitMarketplaceTrusted(ctx context.Context, m MarketplaceCommit) error {
	if m.Authority != nil {
		return plugincmd.ErrInvalid
	}
	return s.commitMarketplace(ctx, m)
}

func (s *sqlPluginManagementStore) commitMarketplace(ctx context.Context, m MarketplaceCommit) error {
	if !normalizedID(m.Name) || m.ExpectedState == "" || !normalizedID(m.AuditID) || m.At.IsZero() {
		return plugincmd.ErrInvalid
	}
	if (m.DefinitionJSON != nil && !pluginJSONObject(m.DefinitionJSON)) || (m.MetadataJSON != nil && !pluginJSONObject(m.MetadataJSON)) {
		return plugincmd.ErrInvalid
	}
	switch m.Operation {
	case marketplaceOpAdd, marketplaceOpRefresh, marketplaceOpRefreshFailed, marketplaceOpUpsert:
		if m.DefinitionJSON == nil {
			return plugincmd.ErrInvalid
		}
	case pluginOpRemove:
		if m.DefinitionJSON != nil || m.MetadataJSON != nil {
			return plugincmd.ErrInvalid
		}
	default:
		return plugincmd.ErrInvalid
	}
	if m.Operation == marketplaceOpUpsert && m.Authority != nil {
		return plugincmd.ErrInvalid
	}
	audit := pluginAudit(m.Authority, m.AuditID, marketplaceAuditAction(m.Operation), usercmd.AuditTargetPluginMarketplace, m.Name, m.At)
	if m.Operation == marketplaceOpRefreshFailed {
		audit.Reason = "Discovery failed"
	}
	return s.transaction(ctx, m.Authority, audit, func(tx *sql.Tx) error {
		if err := s.lockGeneration(ctx, tx, marketplaceGenerationPrefix+m.Name, m.At); err != nil {
			return err
		}
		observed, err := s.observeMarketplace(ctx, tx, m.Name)
		if err != nil {
			return err
		}
		if observed.Token() != m.ExpectedState {
			return plugincmd.ErrConflict
		}
		switch m.Operation {
		case marketplaceOpAdd:
			if observed.DefinitionJSON != nil {
				return plugincmd.ErrConflict
			}
		case marketplaceOpRefresh, marketplaceOpRefreshFailed:
			// Refresh cannot silently replace the source definition.
			if observed.DefinitionJSON == nil || !samePluginJSON(observed.DefinitionJSON, m.DefinitionJSON) {
				return plugincmd.ErrConflict
			}
		case pluginOpRemove:
			if observed.DefinitionJSON == nil {
				return plugincmd.ErrConflict
			}
		}
		if err := s.putPrivateJSON(ctx, tx, marketplaceDefinitionPrefix+m.Name, m.DefinitionJSON, m.At); err != nil {
			return err
		}
		if err := s.putPrivateJSON(ctx, tx, marketplaceMetadataPrefix+m.Name, m.MetadataJSON, m.At); err != nil {
			return err
		}
		return s.advanceGeneration(ctx, tx, marketplaceGenerationPrefix+m.Name, observed.Generation, m.At)
	})
}

func (s *sqlPluginManagementStore) transaction(ctx context.Context, authority *plugincmd.Authority, audit usercmd.AuditEvent, work func(*sql.Tx) error) error {
	if authority != nil && (!validPluginAuthority(*authority) || !authority.At.Equal(audit.OccurredAt)) {
		return plugincmd.ErrInvalid
	}
	if usercmd.ValidateAuditEvent(audit) != nil {
		return plugincmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	var locked lockedBrowserAuthority
	if authority != nil {
		locked, err = s.users.checkBrowserAuthority(ctx, tx, pluginBrowserAuthority(*authority))
		if err != nil {
			return pluginAuthorityError(err)
		}
	}
	if err := work(tx); err != nil {
		return err
	}
	if authority != nil {
		if err := locked.checkTime(pluginBrowserAuthority(*authority)); err != nil {
			return pluginAuthorityError(err)
		}
	}
	if err := s.users.insertAudit(ctx, tx, audit); err != nil {
		return plugincmd.ErrUnavailable
	}
	if authority != nil {
		if err := locked.checkTime(pluginBrowserAuthority(*authority)); err != nil {
			return pluginAuthorityError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func (s *sqlPluginManagementStore) observeMarketplace(ctx context.Context, tx *sql.Tx, name string) (MarketplaceObservation, error) {
	result := MarketplaceObservation{Name: name}
	var err error
	result.Generation, err = s.readGeneration(ctx, tx, marketplaceGenerationPrefix+name)
	if err != nil {
		return result, err
	}
	result.DefinitionJSON, err = s.readPrivateJSON(ctx, tx, marketplaceDefinitionPrefix+name)
	if err != nil {
		return result, err
	}
	result.MetadataJSON, err = s.readPrivateJSON(ctx, tx, marketplaceMetadataPrefix+name)
	if err == nil && ((result.DefinitionJSON != nil && !pluginJSONObject(result.DefinitionJSON)) || (result.MetadataJSON != nil && !pluginJSONObject(result.MetadataJSON))) {
		return result, plugincmd.ErrUnavailable
	}
	return result, err
}

func (s *sqlPluginManagementStore) readPrivateJSON(ctx context.Context, tx *sql.Tx, key string) (json.RawMessage, error) {
	var raw string
	err := tx.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace=? AND key=?`)+s.users.forUpdate, NamespaceApp, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil || !json.Valid([]byte(raw)) {
		return nil, plugincmd.ErrUnavailable
	}
	return json.RawMessage(raw), nil
}

func (s *sqlPluginManagementStore) putPrivateJSON(ctx context.Context, tx *sql.Tx, key string, raw json.RawMessage, at time.Time) error {
	var err error
	if raw == nil {
		_, err = tx.ExecContext(ctx, s.users.bind(`DELETE FROM balda_app_kv WHERE namespace=? AND key=?`), NamespaceApp, key)
	} else {
		_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_app_kv (namespace,key,value_json,expires_at,updated_at) VALUES (?,?,?,NULL,?) ON CONFLICT(namespace,key) DO UPDATE SET value_json=excluded.value_json,expires_at=NULL,updated_at=excluded.updated_at`), NamespaceApp, key, string(raw), formatPluginTime(at))
	}
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func (s *sqlPluginManagementStore) readGeneration(ctx context.Context, tx *sql.Tx, key string) (uint64, error) {
	raw, err := s.readPrivateJSON(ctx, tx, key)
	if err != nil {
		return 0, err
	}
	var generation uint64
	if raw != nil && json.Unmarshal(raw, &generation) != nil {
		return 0, plugincmd.ErrUnavailable
	}
	return generation, nil
}

func (s *sqlPluginManagementStore) lockGeneration(ctx context.Context, tx *sql.Tx, key string, at time.Time) error {
	// The persistent zero sentinel also serializes first creation and absence races.
	_, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_app_kv (namespace,key,value_json,expires_at,updated_at) VALUES (?,?,'0',NULL,?) ON CONFLICT(namespace,key) DO NOTHING`), NamespaceApp, key, formatPluginTime(at))
	if err != nil {
		return plugincmd.ErrUnavailable
	}
	var raw string
	if err := tx.QueryRowContext(ctx, s.users.bind(`SELECT value_json FROM balda_app_kv WHERE namespace=? AND key=?`)+s.users.forUpdate, NamespaceApp, key).Scan(&raw); err != nil {
		return plugincmd.ErrUnavailable
	}
	return nil
}

func (s *sqlPluginManagementStore) advanceGeneration(ctx context.Context, tx *sql.Tx, key string, generation uint64, at time.Time) error {
	if generation == math.MaxUint64 {
		return plugincmd.ErrConflict
	}
	raw, _ := json.Marshal(generation + 1)
	return s.putPrivateJSON(ctx, tx, key, raw, at)
}

func validPluginAuthority(a plugincmd.Authority) bool {
	return strings.TrimSpace(a.UserID) != "" && a.UserVersion != 0 && a.CredentialVersion != 0 && strings.TrimSpace(a.SessionID) != "" && a.SessionVersion != 0 && !a.At.IsZero()
}

func pluginBrowserAuthority(a plugincmd.Authority) browserAuthority {
	return browserAuthority{userID: a.UserID, sessionID: a.SessionID, userVersion: a.UserVersion, credentialVersion: a.CredentialVersion, mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: a.At}
}

func pluginAuthorityError(err error) error {
	switch {
	case errors.Is(err, usercmd.ErrConflict):
		return plugincmd.ErrConflict
	case errors.Is(err, usercmd.ErrForbidden), errors.Is(err, usercmd.ErrNotFound), errors.Is(err, usercmd.ErrSessionUnavailable):
		return plugincmd.ErrForbidden
	default:
		return plugincmd.ErrUnavailable
	}
}

func pluginAudit(a *plugincmd.Authority, id string, action usercmd.AuditAction, target usercmd.AuditTargetType, name string, at time.Time) usercmd.AuditEvent {
	event := usercmd.AuditEvent{ID: id, Action: action, Outcome: usercmd.AuditOutcomeSucceeded, TargetType: target, TargetID: name, Source: "chat", OccurredAt: at}
	if a != nil {
		event.ActorUserID = a.UserID
		event.ActorSessionID = a.SessionID
		event.Source = "backoffice"
	}
	return event
}

func marketplaceAuditAction(operation string) usercmd.AuditAction {
	switch operation {
	case marketplaceOpAdd, marketplaceOpUpsert:
		return usercmd.AuditActionPluginMarketplaceAdded
	case marketplaceOpRefresh:
		return usercmd.AuditActionPluginMarketplaceRefreshed
	case marketplaceOpRefreshFailed:
		return usercmd.AuditActionPluginMarketplaceRefreshFailed
	default:
		return usercmd.AuditActionPluginMarketplaceRemoved
	}
}

func samePluginJSON(a, b json.RawMessage) bool {
	var x, y any
	da, db := json.NewDecoder(bytes.NewReader(a)), json.NewDecoder(bytes.NewReader(b))
	da.UseNumber()
	db.UseNumber()
	if da.Decode(&x) != nil || db.Decode(&y) != nil {
		return false
	}
	cx, _ := json.Marshal(x)
	cy, _ := json.Marshal(y)
	return string(cx) == string(cy)
}

func pluginJSONObject(data []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(data, &object) == nil && object != nil
}
