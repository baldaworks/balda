package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"maps"
	"strings"
	"time"

	"google.golang.org/adk/v2/platform"
	adksession "google.golang.org/adk/v2/session"
)

type postgresRuntimeSessionService struct {
	db *sql.DB
}

// UpdateSessionState updates stored session-scoped state without appending an
// event. Balda uses this to refresh runtime CWD when restoring a persisted chat.
func (s *postgresRuntimeSessionService) UpdateSessionState(
	ctx context.Context,
	appName string,
	userID string,
	sessionID string,
	state map[string]any,
) (adksession.Session, error) {
	key, err := validateRuntimeSessionKey(appName, userID, sessionID)
	if err != nil {
		return nil, err
	}

	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return nil, postgresErrorf("begin update runtime session state: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, updatedAt, err := postgresFetchRuntimeSessionState(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	maps.Copy(sessionState, cloneStateMap(state))
	now := platform.Now(ctx).UTC()
	if err := postgresSaveRuntimeSessionState(ctx, tx, key, sessionState, now); err != nil {
		return nil, err
	}
	if updatedAt.After(now) {
		now = updatedAt
	}
	if err := tx.Commit(); err != nil {
		return nil, postgresErrorf("commit update runtime session state: %w", err)
	}
	return s.sessionFromStorage(ctx, s.db, key, sessionState, now, nil)
}

func (s *postgresRuntimeSessionService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" {
		return nil, postgresErrorf("app_name and user_id are required")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = platform.NewUUID(ctx)
	}
	key := runtimeSessionKey{
		appName:   strings.TrimSpace(req.AppName),
		userID:    strings.TrimSpace(req.UserID),
		sessionID: sessionID,
	}

	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return nil, postgresErrorf("begin create runtime session: %w", err)
	}
	defer rollbackTx(tx)

	var one int
	err = tx.QueryRowContext(ctx, postgresBind(`
		SELECT 1
		FROM balda_runtime_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`), key.appName, key.userID, key.sessionID,
	).Scan(&one)
	if err == nil {
		return nil, postgresErrorf("runtime session %q already exists", key.sessionID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, postgresErrorf("check runtime session %q exists: %w", key.sessionID, err)
	}

	appState, err := postgresFetchRuntimeAppState(ctx, tx, key.appName)
	if err != nil {
		return nil, err
	}
	userState, err := postgresFetchRuntimeUserState(ctx, tx, key.appName, key.userID)
	if err != nil {
		return nil, err
	}

	appDelta, userDelta, sessionState := splitRuntimeStateDeltas(req.State)
	if len(appDelta) > 0 {
		maps.Copy(appState, appDelta)
		if err := postgresSaveRuntimeAppState(ctx, tx, key.appName, appState, platform.Now(ctx).UTC()); err != nil {
			return nil, err
		}
	}
	if len(userDelta) > 0 {
		maps.Copy(userState, userDelta)
		if err := postgresSaveRuntimeUserState(ctx, tx, key.appName, key.userID, userState, platform.Now(ctx).UTC()); err != nil {
			return nil, err
		}
	}

	now := platform.Now(ctx).UTC()
	if err := postgresSaveRuntimeSessionState(ctx, tx, key, sessionState, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, postgresErrorf("commit create runtime session: %w", err)
	}

	return &adksession.CreateResponse{
		Session: newSQLiteRuntimeSession(key, mergeRuntimeStates(appState, userState, sessionState), nil, now),
	}, nil
}

func (s *postgresRuntimeSessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	key, err := validateRuntimeSessionKey(req.AppName, req.UserID, req.SessionID)
	if err != nil {
		return nil, err
	}
	sess, err := s.loadSession(ctx, key, req.NumRecentEvents, req.After)
	if err != nil {
		return nil, err
	}
	return &adksession.GetResponse{Session: sess}, nil
}

func (s *postgresRuntimeSessionService) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		return nil, postgresErrorf("app_name is required")
	}

	query := `
		SELECT user_id, session_id, state_json, updated_at
		FROM balda_runtime_sessions
		WHERE app_name = ?`
	args := []any{appName}
	if userID := strings.TrimSpace(req.UserID); userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.QueryContext(ctx, postgresBind(query), args...)
	if err != nil {
		return nil, postgresErrorf("list runtime sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	appState, err := postgresFetchRuntimeAppState(ctx, s.db, appName)
	if err != nil {
		return nil, err
	}

	type listedSession struct {
		userID     string
		sessionID  string
		stateJSON  string
		updatedRaw string
	}
	listed := make([]listedSession, 0)
	for rows.Next() {
		var item listedSession
		if err := rows.Scan(&item.userID, &item.sessionID, &item.stateJSON, &item.updatedRaw); err != nil {
			return nil, postgresErrorf("scan runtime session: %w", err)
		}
		listed = append(listed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate runtime sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, postgresErrorf("close runtime session rows: %w", err)
	}

	out := make([]adksession.Session, 0, len(listed))
	for _, item := range listed {
		sessionState, err := decodeStateMap(item.stateJSON)
		if err != nil {
			return nil, postgresErrorf("decode runtime session %q state: %w", item.sessionID, err)
		}
		updatedAt, err := parseRuntimeTime(item.updatedRaw)
		if err != nil {
			return nil, postgresErrorf("parse runtime session %q update time: %w", item.sessionID, err)
		}
		userState, err := postgresFetchRuntimeUserState(ctx, s.db, appName, item.userID)
		if err != nil {
			return nil, err
		}
		out = append(out, newSQLiteRuntimeSession(
			runtimeSessionKey{appName: appName, userID: item.userID, sessionID: item.sessionID},
			mergeRuntimeStates(appState, userState, sessionState),
			nil,
			updatedAt,
		))
	}

	return &adksession.ListResponse{Sessions: out}, nil
}

func (s *postgresRuntimeSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	key, err := validateRuntimeSessionKey(req.AppName, req.UserID, req.SessionID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, postgresBind(`
		DELETE FROM balda_runtime_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`), key.appName, key.userID, key.sessionID,
	); err != nil {
		return postgresErrorf("delete runtime session %q: %w", key.sessionID, err)
	}
	return nil
}

func (s *postgresRuntimeSessionService) AppendEvent(ctx context.Context, curSession adksession.Session, event *adksession.Event) error {
	if curSession == nil {
		return postgresErrorf("session is nil")
	}
	if event == nil {
		return postgresErrorf("event is nil")
	}
	if event.Partial {
		return nil
	}
	key, err := validateRuntimeSessionKey(curSession.AppName(), curSession.UserID(), curSession.ID())
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = platform.NewUUID(ctx)
	}
	event.Timestamp = event.Timestamp.UTC().Truncate(time.Microsecond)
	if event.Timestamp.IsZero() {
		event.Timestamp = platform.Now(ctx).UTC().Truncate(time.Microsecond)
	}
	filterTempState(event)

	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return postgresErrorf("begin append runtime event: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, _, err := postgresFetchRuntimeSessionState(ctx, tx, key)
	if err != nil {
		return err
	}
	appState, err := postgresFetchRuntimeAppState(ctx, tx, key.appName)
	if err != nil {
		return err
	}
	userState, err := postgresFetchRuntimeUserState(ctx, tx, key.appName, key.userID)
	if err != nil {
		return err
	}
	appDelta, userDelta, sessionDelta := splitRuntimeStateDeltas(event.Actions.StateDelta)
	if len(appDelta) > 0 {
		maps.Copy(appState, appDelta)
		if err := postgresSaveRuntimeAppState(ctx, tx, key.appName, appState, event.Timestamp); err != nil {
			return err
		}
	}
	if len(userDelta) > 0 {
		maps.Copy(userState, userDelta)
		if err := postgresSaveRuntimeUserState(ctx, tx, key.appName, key.userID, userState, event.Timestamp); err != nil {
			return err
		}
	}
	if len(sessionDelta) > 0 {
		maps.Copy(sessionState, sessionDelta)
	}

	var ordinal sql.NullInt64
	err = tx.QueryRowContext(ctx, postgresBind(`
		SELECT COALESCE(MAX(ordinal), 0) + 1
		FROM balda_runtime_events
		WHERE app_name = ? AND user_id = ? AND session_id = ?`), key.appName, key.userID, key.sessionID,
	).Scan(&ordinal)
	if err != nil {
		return postgresErrorf("next runtime event ordinal: %w", err)
	}
	eventOrdinal := int64(1)
	if ordinal.Valid {
		eventOrdinal = ordinal.Int64
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return postgresErrorf("marshal runtime event %q: %w", event.ID, err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_runtime_events (
			app_name, user_id, session_id, event_id, ordinal, timestamp, event_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)`), key.appName,
		key.userID,
		key.sessionID,
		event.ID,
		eventOrdinal,
		event.Timestamp.Format(runtimeSessionTimeFormat),
		string(eventJSON),
	); err != nil {
		return postgresErrorf("insert runtime event %q: %w", event.ID, err)
	}
	if err := postgresSaveRuntimeSessionState(ctx, tx, key, sessionState, event.Timestamp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return postgresErrorf("commit append runtime event: %w", err)
	}

	if sess, ok := curSession.(*sqliteRuntimeSession); ok {
		sess.appendEvent(event, mergeRuntimeStates(appState, userState, sessionState), event.Timestamp)
	}
	return nil
}

func (s *postgresRuntimeSessionService) loadSession(ctx context.Context, key runtimeSessionKey, limit int, after time.Time) (*sqliteRuntimeSession, error) {
	tx, err := beginPostgresTx(s.db, ctx, nil)
	if err != nil {
		return nil, postgresErrorf("begin get runtime session: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, updatedAt, err := postgresFetchRuntimeSessionState(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	events, err := postgresFetchRuntimeEvents(ctx, tx, key, limit, after)
	if err != nil {
		return nil, err
	}
	sess, err := s.sessionFromStorage(ctx, tx, key, sessionState, updatedAt, events)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, postgresErrorf("commit get runtime session: %w", err)
	}
	return sess, nil
}

func (s *postgresRuntimeSessionService) sessionFromStorage(
	ctx context.Context,
	q dbQueryer,
	key runtimeSessionKey,
	sessionState map[string]any,
	updatedAt time.Time,
	events []*adksession.Event,
) (*sqliteRuntimeSession, error) {
	appState, err := postgresFetchRuntimeAppState(ctx, q, key.appName)
	if err != nil {
		return nil, err
	}
	userState, err := postgresFetchRuntimeUserState(ctx, q, key.appName, key.userID)
	if err != nil {
		return nil, err
	}
	return newSQLiteRuntimeSession(key, mergeRuntimeStates(appState, userState, sessionState), events, updatedAt), nil
}

func postgresFetchRuntimeSessionState(ctx context.Context, q dbQueryer, key runtimeSessionKey) (map[string]any, time.Time, error) {
	var stateJSON, updatedRaw string
	err := q.QueryRowContext(ctx, postgresBind(`
		SELECT state_json, updated_at
		FROM balda_runtime_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`), key.appName, key.userID, key.sessionID,
	).Scan(&stateJSON, &updatedRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, time.Time{}, postgresErrorf("runtime session %q not found", key.sessionID)
		}
		return nil, time.Time{}, postgresErrorf("fetch runtime session %q: %w", key.sessionID, err)
	}
	state, err := decodeStateMap(stateJSON)
	if err != nil {
		return nil, time.Time{}, postgresErrorf("decode runtime session %q state: %w", key.sessionID, err)
	}
	updatedAt, err := parseRuntimeTime(updatedRaw)
	if err != nil {
		return nil, time.Time{}, postgresErrorf("parse runtime session %q update time: %w", key.sessionID, err)
	}
	return state, updatedAt, nil
}

func postgresSaveRuntimeSessionState(ctx context.Context, tx *sql.Tx, key runtimeSessionKey, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return postgresErrorf("encode runtime session %q state: %w", key.sessionID, err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_runtime_sessions (app_name, user_id, session_id, state_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(app_name, user_id, session_id) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`), key.appName, key.userID, key.sessionID, stateJSON, updatedAt.UTC().Format(runtimeSessionTimeFormat),
	); err != nil {
		return postgresErrorf("save runtime session %q state: %w", key.sessionID, err)
	}
	return nil
}

func postgresFetchRuntimeAppState(ctx context.Context, q dbQueryer, appName string) (map[string]any, error) {
	var raw string
	err := q.QueryRowContext(ctx, postgresBind(`
		SELECT state_json
		FROM balda_runtime_app_state
		WHERE app_name = ?`), strings.TrimSpace(appName),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{}, nil
		}
		return nil, postgresErrorf("fetch runtime app state: %w", err)
	}
	return decodeStateMap(raw)
}

func postgresSaveRuntimeAppState(ctx context.Context, tx *sql.Tx, appName string, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return postgresErrorf("encode runtime app state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_runtime_app_state (app_name, state_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(app_name) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`), strings.TrimSpace(appName), stateJSON, updatedAt.UTC().Format(runtimeSessionTimeFormat),
	); err != nil {
		return postgresErrorf("save runtime app state: %w", err)
	}
	return nil
}

func postgresFetchRuntimeUserState(ctx context.Context, q dbQueryer, appName, userID string) (map[string]any, error) {
	var raw string
	err := q.QueryRowContext(ctx, postgresBind(`
		SELECT state_json
		FROM balda_runtime_user_state
		WHERE app_name = ? AND user_id = ?`), strings.TrimSpace(appName), strings.TrimSpace(userID),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{}, nil
		}
		return nil, postgresErrorf("fetch runtime user state: %w", err)
	}
	return decodeStateMap(raw)
}

func postgresSaveRuntimeUserState(ctx context.Context, tx *sql.Tx, appName, userID string, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return postgresErrorf("encode runtime user state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, postgresBind(`
		INSERT INTO balda_runtime_user_state (app_name, user_id, state_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(app_name, user_id) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`), strings.TrimSpace(appName), strings.TrimSpace(userID), stateJSON, updatedAt.UTC().Format(runtimeSessionTimeFormat),
	); err != nil {
		return postgresErrorf("save runtime user state: %w", err)
	}
	return nil
}

func postgresFetchRuntimeEvents(ctx context.Context, q dbQueryer, key runtimeSessionKey, limit int, after time.Time) ([]*adksession.Event, error) {
	query := `
		SELECT event_json
		FROM balda_runtime_events
		WHERE app_name = ? AND user_id = ? AND session_id = ?`
	args := []any{key.appName, key.userID, key.sessionID}
	if !after.IsZero() {
		query += postgresAfterClause
		args = append(args, after.UTC().Format(runtimeSessionTimeFormat))
	}
	if limit > 0 {
		afterClause := ""
		if !after.IsZero() {
			afterClause = postgresAfterClause
		}
		query = `
			SELECT event_json
			FROM (
				SELECT event_json, timestamp, ordinal
				FROM balda_runtime_events
				WHERE app_name = ? AND user_id = ? AND session_id = ?` + afterClause + `
				ORDER BY timestamp DESC, ordinal DESC
				LIMIT ?
			)
			ORDER BY timestamp ASC, ordinal ASC`
		args = []any{key.appName, key.userID, key.sessionID}
		if !after.IsZero() {
			args = append(args, after.UTC().Format(runtimeSessionTimeFormat))
		}
		args = append(args, limit)
	} else {
		query += ` ORDER BY timestamp ASC, ordinal ASC`
	}

	rows, err := q.QueryContext(ctx, postgresBind(query), args...)
	if err != nil {
		return nil, postgresErrorf("fetch runtime events for %q: %w", key.sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]*adksession.Event, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, postgresErrorf("scan runtime event: %w", err)
		}
		var event adksession.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, postgresErrorf("decode runtime event: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, postgresErrorf("iterate runtime events: %w", err)
	}
	return events, nil
}
