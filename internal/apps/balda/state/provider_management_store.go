package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/providers"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

type sqlProviderManagementStore struct {
	db        *sql.DB
	bind      func(string) string
	begin     func(context.Context, *sql.TxOptions) (*sql.Tx, error)
	wrapError func(string, error) error
	forUpdate string
	users     *sqlUserStore
}

var _ providers.Store = (*sqlProviderManagementStore)(nil)

func newSQLiteProviderManagementStore(db *sql.DB) *sqlProviderManagementStore {
	return &sqlProviderManagementStore{
		db: db, bind: func(query string) string { return query },
		begin:     func(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) { return db.BeginTx(ctx, options) },
		wrapError: func(operation string, err error) error { return fmt.Errorf("%s: %w", operation, err) },
		users:     newSQLiteUserStore(db),
	}
}

func newPostgresProviderManagementStore(db *sql.DB) *sqlProviderManagementStore {
	return &sqlProviderManagementStore{
		db: db, bind: postgresBind, begin: func(ctx context.Context, options *sql.TxOptions) (*sql.Tx, error) {
			return beginPostgresTx(db, ctx, options)
		},
		wrapError: func(operation string, err error) error { return postgresErrorf("%s: %w", operation, err) },
		forUpdate: " FOR UPDATE", users: newPostgresUserStore(db),
	}
}

func (s *sqlProviderManagementStore) Read(ctx context.Context) (providers.State, error) {
	tx, err := s.begin(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return providers.State{}, s.wrapError("begin provider state read", err)
	}
	defer func() { _ = tx.Rollback() }()

	state, err := s.readState(ctx, tx)
	if err != nil {
		return providers.State{}, err
	}
	if err := tx.Commit(); err != nil {
		return providers.State{}, s.wrapError("commit provider state read", err)
	}
	return state, nil
}

func (s *sqlProviderManagementStore) Apply(ctx context.Context, mutation providers.Mutation) (providers.State, error) {
	if err := validateProviderMutation(mutation); err != nil {
		return providers.State{}, err
	}
	tx, err := s.begin(ctx, nil)
	if err != nil {
		return providers.State{}, s.wrapError("begin provider mutation", err)
	}
	defer func() { _ = tx.Rollback() }()

	var version uint64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT state_version FROM balda_provider_state WHERE singleton = 1`)+s.forUpdate).Scan(&version); err != nil {
		return providers.State{}, s.wrapError("load provider state version", err)
	}
	if providers.StateVersion(version) != mutation.ExpectedVersion {
		return providers.State{}, providers.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, s.bind(`
		INSERT INTO balda_provider_records(provider_id,current_revision_id,enabled,deleted)
		VALUES(?,?,?,?)
		ON CONFLICT(provider_id) DO UPDATE SET current_revision_id=excluded.current_revision_id,enabled=excluded.enabled,deleted=excluded.deleted`),
		mutation.Record.ID, mutation.Record.CurrentRevisionID, boolInt(mutation.Record.Enabled), boolInt(mutation.Record.Deleted)); err != nil {
		return providers.State{}, s.wrapError("write provider record", err)
	}
	if mutation.Revision != nil {
		definition, marshalErr := json.Marshal(mutation.Revision.Definition)
		if marshalErr != nil {
			return providers.State{}, providers.ErrInvalid
		}
		protected := mutation.Revision.ProtectedValues
		if protected == nil {
			protected = []byte{}
		}
		if _, err := tx.ExecContext(ctx, s.bind(`
			INSERT INTO balda_provider_revisions(provider_id,revision_id,driver_type,contract_version,format_version,definition_json,protected_values,created_at)
			VALUES(?,?,?,?,?,?,?,?)`), mutation.Revision.ProviderID, mutation.Revision.ID, mutation.Revision.DriverType,
			mutation.Revision.ContractVersion, mutation.Revision.FormatVersion, string(definition), protected,
			mutation.Revision.CreatedAt.UTC().Format(time.RFC3339Nano)); err != nil {
			return providers.State{}, s.wrapError("write provider revision", err)
		}
	}
	result, err := tx.ExecContext(ctx, s.bind(`UPDATE balda_provider_state SET state_version = state_version + 1 WHERE singleton = 1 AND state_version = ?`), version)
	if err != nil {
		return providers.State{}, s.wrapError("advance provider state version", err)
	}
	if affected, rowsErr := result.RowsAffected(); rowsErr != nil {
		return providers.State{}, s.wrapError("inspect provider state version", rowsErr)
	} else if affected != 1 {
		return providers.State{}, providers.ErrConflict
	}
	if err := s.users.insertAudit(ctx, tx, mutation.Audit); err != nil {
		return providers.State{}, err
	}
	state, err := s.readState(ctx, tx)
	if err != nil {
		return providers.State{}, err
	}
	if err := tx.Commit(); err != nil {
		return providers.State{}, s.wrapError("commit provider mutation", err)
	}
	return state, nil
}

func (s *sqlProviderManagementStore) Revisions(ctx context.Context, refs []providers.RevisionRef) ([]providers.Revision, error) {
	if len(refs) > 64 {
		return nil, providers.ErrInvalid
	}
	tx, err := s.begin(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, s.wrapError("begin provider revision read", err)
	}
	defer func() { _ = tx.Rollback() }()
	result := make([]providers.Revision, 0, len(refs))
	for _, ref := range refs {
		revision, scanErr := s.scanRevision(tx.QueryRowContext(ctx, s.bind(`
			SELECT provider_id,revision_id,driver_type,contract_version,format_version,definition_json,protected_values,created_at
			FROM balda_provider_revisions WHERE provider_id=? AND revision_id=?`), ref.ProviderID, ref.RevisionID))
		if errors.Is(scanErr, sql.ErrNoRows) {
			return nil, providers.ErrUnavailable
		}
		if scanErr != nil {
			return nil, s.wrapError("read provider revision", scanErr)
		}
		result = append(result, revision)
	}
	if err := tx.Commit(); err != nil {
		return nil, s.wrapError("commit provider revision read", err)
	}
	return result, nil
}

func (s *sqlProviderManagementStore) readState(ctx context.Context, tx *sql.Tx) (providers.State, error) {
	var version uint64
	if err := tx.QueryRowContext(ctx, s.bind(`SELECT state_version FROM balda_provider_state WHERE singleton = 1`)).Scan(&version); err != nil {
		return providers.State{}, s.wrapError("read provider state version", err)
	}
	state := providers.State{Version: providers.StateVersion(version)}
	rows, err := tx.QueryContext(ctx, s.bind(`SELECT provider_id,current_revision_id,enabled,deleted FROM balda_provider_records ORDER BY provider_id`))
	if err != nil {
		return providers.State{}, s.wrapError("list provider records", err)
	}
	for rows.Next() {
		var record providers.Record
		var enabled, deleted int
		if err := rows.Scan(&record.ID, &record.CurrentRevisionID, &enabled, &deleted); err != nil {
			_ = rows.Close()
			return providers.State{}, s.wrapError("scan provider record", err)
		}
		record.Enabled, record.Deleted = intBool(enabled), intBool(deleted)
		state.Records = append(state.Records, record)
	}
	if err := rows.Close(); err != nil {
		return providers.State{}, s.wrapError("close provider records", err)
	}
	if err := rows.Err(); err != nil {
		return providers.State{}, s.wrapError("iterate provider records", err)
	}
	revisions, err := tx.QueryContext(ctx, s.bind(`
		SELECT r.provider_id,r.revision_id,r.driver_type,r.contract_version,r.format_version,r.definition_json,r.protected_values,r.created_at
		FROM balda_provider_revisions r JOIN balda_provider_records p ON p.provider_id=r.provider_id AND p.current_revision_id=r.revision_id
		ORDER BY r.provider_id`))
	if err != nil {
		return providers.State{}, s.wrapError("list current provider revisions", err)
	}
	defer func() { _ = revisions.Close() }()
	for revisions.Next() {
		revision, scanErr := s.scanRevision(revisions)
		if scanErr != nil {
			return providers.State{}, s.wrapError("scan provider revision", scanErr)
		}
		state.Revisions = append(state.Revisions, revision)
	}
	if err := revisions.Err(); err != nil {
		return providers.State{}, s.wrapError("iterate provider revisions", err)
	}
	return state, nil
}

func (s *sqlProviderManagementStore) scanRevision(row rowScanner) (providers.Revision, error) {
	var revision providers.Revision
	var definition, createdAt string
	if err := row.Scan(&revision.ProviderID, &revision.ID, &revision.DriverType, &revision.ContractVersion, &revision.FormatVersion,
		&definition, &revision.ProtectedValues, &createdAt); err != nil {
		return providers.Revision{}, err
	}
	if err := json.Unmarshal([]byte(definition), &revision.Definition); err != nil {
		return providers.Revision{}, providers.ErrUnavailable
	}
	parsed, err := time.Parse(time.RFC3339Nano, createdAt)
	if err != nil {
		return providers.Revision{}, providers.ErrUnavailable
	}
	revision.CreatedAt = parsed
	return revision, nil
}

func validateProviderMutation(mutation providers.Mutation) error {
	if mutation.Record.ID == "" || mutation.Record.CurrentRevisionID == "" ||
		mutation.Revision != nil && (mutation.Revision.ID != mutation.Record.CurrentRevisionID || mutation.Revision.ProviderID != mutation.Record.ID) {
		return providers.ErrInvalid
	}
	if err := usercmd.ValidateAuditEvent(mutation.Audit); err != nil || mutation.Audit.Action != usercmd.AuditActionProviderChanged ||
		mutation.Audit.TargetType != usercmd.AuditTargetProvider || mutation.Audit.TargetID != mutation.Record.ID || mutation.Audit.Outcome != usercmd.AuditOutcomeSucceeded {
		return providers.ErrInvalid
	}
	return nil
}
