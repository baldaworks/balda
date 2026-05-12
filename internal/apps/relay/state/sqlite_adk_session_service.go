package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"maps"
	"strings"
	"time"

	"github.com/google/uuid"
	adksession "google.golang.org/adk/session"
)

const adkSessionTimeFormat = time.RFC3339Nano

type sqliteADKSessionService struct {
	db *sql.DB
}

var _ adksession.Service = (*sqliteADKSessionService)(nil)

// UpdateSessionState updates stored session-scoped state without appending an
// event. Relay uses this to refresh runtime CWD when restoring a persisted chat.
func (s *sqliteADKSessionService) UpdateSessionState(
	ctx context.Context,
	appName string,
	userID string,
	sessionID string,
	state map[string]any,
) (adksession.Session, error) {
	key, err := validateADKSessionKey(appName, userID, sessionID)
	if err != nil {
		return nil, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin update adk session state: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, updatedAt, err := fetchADKSessionState(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	maps.Copy(sessionState, cloneStateMap(state))
	now := time.Now().UTC()
	if err := saveADKSessionState(ctx, tx, key, sessionState, now); err != nil {
		return nil, err
	}
	if updatedAt.After(now) {
		now = updatedAt
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit update adk session state: %w", err)
	}
	return s.sessionFromStorage(ctx, s.db, key, sessionState, now, nil)
}

func (s *sqliteADKSessionService) Create(ctx context.Context, req *adksession.CreateRequest) (*adksession.CreateResponse, error) {
	if strings.TrimSpace(req.AppName) == "" || strings.TrimSpace(req.UserID) == "" {
		return nil, fmt.Errorf("app_name and user_id are required")
	}

	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		sessionID = uuid.NewString()
	}
	key := adkSessionKey{
		appName:   strings.TrimSpace(req.AppName),
		userID:    strings.TrimSpace(req.UserID),
		sessionID: sessionID,
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin create adk session: %w", err)
	}
	defer rollbackTx(tx)

	if exists, err := adkSessionExists(ctx, tx, key); err != nil {
		return nil, err
	} else if exists {
		return nil, fmt.Errorf("adk session %q already exists", key.sessionID)
	}

	appState, err := fetchADKAppState(ctx, tx, key.appName)
	if err != nil {
		return nil, err
	}
	userState, err := fetchADKUserState(ctx, tx, key.appName, key.userID)
	if err != nil {
		return nil, err
	}

	appDelta, userDelta, sessionState := splitADKStateDeltas(req.State)
	if len(appDelta) > 0 {
		maps.Copy(appState, appDelta)
		if err := saveADKAppState(ctx, tx, key.appName, appState, time.Now().UTC()); err != nil {
			return nil, err
		}
	}
	if len(userDelta) > 0 {
		maps.Copy(userState, userDelta)
		if err := saveADKUserState(ctx, tx, key.appName, key.userID, userState, time.Now().UTC()); err != nil {
			return nil, err
		}
	}

	now := time.Now().UTC()
	if err := saveADKSessionState(ctx, tx, key, sessionState, now); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit create adk session: %w", err)
	}

	return &adksession.CreateResponse{
		Session: newSQLiteADKSession(key, mergeADKStates(appState, userState, sessionState), nil, now),
	}, nil
}

func (s *sqliteADKSessionService) Get(ctx context.Context, req *adksession.GetRequest) (*adksession.GetResponse, error) {
	key, err := validateADKSessionKey(req.AppName, req.UserID, req.SessionID)
	if err != nil {
		return nil, err
	}
	sess, err := s.loadSession(ctx, key, req.NumRecentEvents, req.After)
	if err != nil {
		return nil, err
	}
	return &adksession.GetResponse{Session: sess}, nil
}

func (s *sqliteADKSessionService) List(ctx context.Context, req *adksession.ListRequest) (*adksession.ListResponse, error) {
	appName := strings.TrimSpace(req.AppName)
	if appName == "" {
		return nil, fmt.Errorf("app_name is required")
	}

	query := `
		SELECT user_id, session_id, state_json, updated_at
		FROM relay_adk_sessions
		WHERE app_name = ?`
	args := []any{appName}
	if userID := strings.TrimSpace(req.UserID); userID != "" {
		query += ` AND user_id = ?`
		args = append(args, userID)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list adk sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	appState, err := fetchADKAppState(ctx, s.db, appName)
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
			return nil, fmt.Errorf("scan adk session: %w", err)
		}
		listed = append(listed, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adk sessions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close adk session rows: %w", err)
	}

	out := make([]adksession.Session, 0, len(listed))
	for _, item := range listed {
		sessionState, err := decodeStateMap(item.stateJSON)
		if err != nil {
			return nil, fmt.Errorf("decode adk session %q state: %w", item.sessionID, err)
		}
		updatedAt, err := parseADKTime(item.updatedRaw)
		if err != nil {
			return nil, fmt.Errorf("parse adk session %q update time: %w", item.sessionID, err)
		}
		userState, err := fetchADKUserState(ctx, s.db, appName, item.userID)
		if err != nil {
			return nil, err
		}
		out = append(out, newSQLiteADKSession(
			adkSessionKey{appName: appName, userID: item.userID, sessionID: item.sessionID},
			mergeADKStates(appState, userState, sessionState),
			nil,
			updatedAt,
		))
	}

	return &adksession.ListResponse{Sessions: out}, nil
}

func (s *sqliteADKSessionService) Delete(ctx context.Context, req *adksession.DeleteRequest) error {
	key, err := validateADKSessionKey(req.AppName, req.UserID, req.SessionID)
	if err != nil {
		return err
	}
	if _, err := s.db.ExecContext(ctx, `
		DELETE FROM relay_adk_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`,
		key.appName, key.userID, key.sessionID,
	); err != nil {
		return fmt.Errorf("delete adk session %q: %w", key.sessionID, err)
	}
	return nil
}

func (s *sqliteADKSessionService) AppendEvent(ctx context.Context, curSession adksession.Session, event *adksession.Event) error {
	if curSession == nil {
		return fmt.Errorf("session is nil")
	}
	if event == nil {
		return fmt.Errorf("event is nil")
	}
	if event.Partial {
		return nil
	}
	key, err := validateADKSessionKey(curSession.AppName(), curSession.UserID(), curSession.ID())
	if err != nil {
		return err
	}
	if strings.TrimSpace(event.ID) == "" {
		event.ID = uuid.NewString()
	}
	event.Timestamp = event.Timestamp.UTC().Truncate(time.Microsecond)
	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC().Truncate(time.Microsecond)
	}
	filterTempState(event)

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin append adk event: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, _, err := fetchADKSessionState(ctx, tx, key)
	if err != nil {
		return err
	}
	appState, err := fetchADKAppState(ctx, tx, key.appName)
	if err != nil {
		return err
	}
	userState, err := fetchADKUserState(ctx, tx, key.appName, key.userID)
	if err != nil {
		return err
	}
	appDelta, userDelta, sessionDelta := splitADKStateDeltas(event.Actions.StateDelta)
	if len(appDelta) > 0 {
		maps.Copy(appState, appDelta)
		if err := saveADKAppState(ctx, tx, key.appName, appState, event.Timestamp); err != nil {
			return err
		}
	}
	if len(userDelta) > 0 {
		maps.Copy(userState, userDelta)
		if err := saveADKUserState(ctx, tx, key.appName, key.userID, userState, event.Timestamp); err != nil {
			return err
		}
	}
	if len(sessionDelta) > 0 {
		maps.Copy(sessionState, sessionDelta)
	}

	ordinal, err := nextADKEventOrdinal(ctx, tx, key)
	if err != nil {
		return err
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal adk event %q: %w", event.ID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_adk_events (
			app_name, user_id, session_id, event_id, ordinal, timestamp, event_json
		)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		key.appName,
		key.userID,
		key.sessionID,
		event.ID,
		ordinal,
		event.Timestamp.Format(adkSessionTimeFormat),
		string(eventJSON),
	); err != nil {
		return fmt.Errorf("insert adk event %q: %w", event.ID, err)
	}
	if err := saveADKSessionState(ctx, tx, key, sessionState, event.Timestamp); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit append adk event: %w", err)
	}

	if sess, ok := curSession.(*sqliteADKSession); ok {
		sess.appendEvent(event, mergeADKStates(appState, userState, sessionState), event.Timestamp)
	}
	return nil
}

func (s *sqliteADKSessionService) loadSession(ctx context.Context, key adkSessionKey, limit int, after time.Time) (*sqliteADKSession, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin get adk session: %w", err)
	}
	defer rollbackTx(tx)

	sessionState, updatedAt, err := fetchADKSessionState(ctx, tx, key)
	if err != nil {
		return nil, err
	}
	events, err := fetchADKEvents(ctx, tx, key, limit, after)
	if err != nil {
		return nil, err
	}
	sess, err := s.sessionFromStorage(ctx, tx, key, sessionState, updatedAt, events)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit get adk session: %w", err)
	}
	return sess, nil
}

func (s *sqliteADKSessionService) sessionFromStorage(
	ctx context.Context,
	q dbQueryer,
	key adkSessionKey,
	sessionState map[string]any,
	updatedAt time.Time,
	events []*adksession.Event,
) (*sqliteADKSession, error) {
	appState, err := fetchADKAppState(ctx, q, key.appName)
	if err != nil {
		return nil, err
	}
	userState, err := fetchADKUserState(ctx, q, key.appName, key.userID)
	if err != nil {
		return nil, err
	}
	return newSQLiteADKSession(key, mergeADKStates(appState, userState, sessionState), events, updatedAt), nil
}

type adkSessionKey struct {
	appName   string
	userID    string
	sessionID string
}

func validateADKSessionKey(appName, userID, sessionID string) (adkSessionKey, error) {
	key := adkSessionKey{
		appName:   strings.TrimSpace(appName),
		userID:    strings.TrimSpace(userID),
		sessionID: strings.TrimSpace(sessionID),
	}
	if key.appName == "" || key.userID == "" || key.sessionID == "" {
		return adkSessionKey{}, fmt.Errorf("app_name, user_id, session_id are required")
	}
	return key, nil
}

type sqliteADKSession struct {
	key       adkSessionKey
	state     *sqliteADKState
	events    *sqliteADKEvents
	updatedAt time.Time
}

func newSQLiteADKSession(key adkSessionKey, state map[string]any, events []*adksession.Event, updatedAt time.Time) *sqliteADKSession {
	return &sqliteADKSession{
		key:       key,
		state:     &sqliteADKState{values: cloneStateMap(state)},
		events:    &sqliteADKEvents{events: cloneADKEvents(events)},
		updatedAt: updatedAt,
	}
}

func (s *sqliteADKSession) ID() string {
	return s.key.sessionID
}

func (s *sqliteADKSession) AppName() string {
	return s.key.appName
}

func (s *sqliteADKSession) UserID() string {
	return s.key.userID
}

func (s *sqliteADKSession) State() adksession.State {
	return s.state
}

func (s *sqliteADKSession) Events() adksession.Events {
	return s.events
}

func (s *sqliteADKSession) LastUpdateTime() time.Time {
	return s.updatedAt
}

func (s *sqliteADKSession) appendEvent(event *adksession.Event, state map[string]any, updatedAt time.Time) {
	s.events.events = append(s.events.events, event)
	s.state.values = cloneStateMap(state)
	s.updatedAt = updatedAt
}

type sqliteADKState struct {
	values map[string]any
}

func (s *sqliteADKState) Get(key string) (any, error) {
	value, ok := s.values[key]
	if !ok {
		return nil, adksession.ErrStateKeyNotExist
	}
	return value, nil
}

func (s *sqliteADKState) Set(key string, value any) error {
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = value
	return nil
}

func (s *sqliteADKState) All() iter.Seq2[string, any] {
	values := cloneStateMap(s.values)
	return func(yield func(string, any) bool) {
		for key, value := range values {
			if !yield(key, value) {
				return
			}
		}
	}
}

type sqliteADKEvents struct {
	events []*adksession.Event
}

func (e *sqliteADKEvents) All() iter.Seq[*adksession.Event] {
	events := cloneADKEvents(e.events)
	return func(yield func(*adksession.Event) bool) {
		for _, event := range events {
			if !yield(event) {
				return
			}
		}
	}
}

func (e *sqliteADKEvents) Len() int {
	return len(e.events)
}

func (e *sqliteADKEvents) At(i int) *adksession.Event {
	return e.events[i]
}

func fetchADKSessionState(ctx context.Context, q dbQueryer, key adkSessionKey) (map[string]any, time.Time, error) {
	var stateJSON, updatedRaw string
	err := q.QueryRowContext(ctx, `
		SELECT state_json, updated_at
		FROM relay_adk_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`,
		key.appName, key.userID, key.sessionID,
	).Scan(&stateJSON, &updatedRaw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, time.Time{}, fmt.Errorf("adk session %q not found", key.sessionID)
		}
		return nil, time.Time{}, fmt.Errorf("fetch adk session %q: %w", key.sessionID, err)
	}
	state, err := decodeStateMap(stateJSON)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("decode adk session %q state: %w", key.sessionID, err)
	}
	updatedAt, err := parseADKTime(updatedRaw)
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("parse adk session %q update time: %w", key.sessionID, err)
	}
	return state, updatedAt, nil
}

func saveADKSessionState(ctx context.Context, tx *sql.Tx, key adkSessionKey, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return fmt.Errorf("encode adk session %q state: %w", key.sessionID, err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_adk_sessions (app_name, user_id, session_id, state_json, updated_at)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(app_name, user_id, session_id) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`,
		key.appName, key.userID, key.sessionID, stateJSON, updatedAt.UTC().Format(adkSessionTimeFormat),
	); err != nil {
		return fmt.Errorf("save adk session %q state: %w", key.sessionID, err)
	}
	return nil
}

func fetchADKAppState(ctx context.Context, q dbQueryer, appName string) (map[string]any, error) {
	var raw string
	err := q.QueryRowContext(ctx, `
		SELECT state_json
		FROM relay_adk_app_state
		WHERE app_name = ?`,
		strings.TrimSpace(appName),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("fetch adk app state: %w", err)
	}
	return decodeStateMap(raw)
}

func saveADKAppState(ctx context.Context, tx *sql.Tx, appName string, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return fmt.Errorf("encode adk app state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_adk_app_state (app_name, state_json, updated_at)
		VALUES (?, ?, ?)
		ON CONFLICT(app_name) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`,
		strings.TrimSpace(appName), stateJSON, updatedAt.UTC().Format(adkSessionTimeFormat),
	); err != nil {
		return fmt.Errorf("save adk app state: %w", err)
	}
	return nil
}

func fetchADKUserState(ctx context.Context, q dbQueryer, appName, userID string) (map[string]any, error) {
	var raw string
	err := q.QueryRowContext(ctx, `
		SELECT state_json
		FROM relay_adk_user_state
		WHERE app_name = ? AND user_id = ?`,
		strings.TrimSpace(appName), strings.TrimSpace(userID),
	).Scan(&raw)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("fetch adk user state: %w", err)
	}
	return decodeStateMap(raw)
}

func saveADKUserState(ctx context.Context, tx *sql.Tx, appName, userID string, state map[string]any, updatedAt time.Time) error {
	stateJSON, err := encodeStateMap(state)
	if err != nil {
		return fmt.Errorf("encode adk user state: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO relay_adk_user_state (app_name, user_id, state_json, updated_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(app_name, user_id) DO UPDATE SET
			state_json = excluded.state_json,
			updated_at = excluded.updated_at`,
		strings.TrimSpace(appName), strings.TrimSpace(userID), stateJSON, updatedAt.UTC().Format(adkSessionTimeFormat),
	); err != nil {
		return fmt.Errorf("save adk user state: %w", err)
	}
	return nil
}

func adkSessionExists(ctx context.Context, q dbQueryer, key adkSessionKey) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, `
		SELECT 1
		FROM relay_adk_sessions
		WHERE app_name = ? AND user_id = ? AND session_id = ?`,
		key.appName, key.userID, key.sessionID,
	).Scan(&one)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, nil
		}
		return false, fmt.Errorf("check adk session %q exists: %w", key.sessionID, err)
	}
	return true, nil
}

func fetchADKEvents(ctx context.Context, q dbQueryer, key adkSessionKey, limit int, after time.Time) ([]*adksession.Event, error) {
	query := `
		SELECT event_json
		FROM relay_adk_events
		WHERE app_name = ? AND user_id = ? AND session_id = ?`
	args := []any{key.appName, key.userID, key.sessionID}
	if !after.IsZero() {
		query += ` AND timestamp >= ?`
		args = append(args, after.UTC().Format(adkSessionTimeFormat))
	}
	if limit > 0 {
		query = `
			SELECT event_json
			FROM (
				SELECT event_json, timestamp, ordinal
				FROM relay_adk_events
				WHERE app_name = ? AND user_id = ? AND session_id = ?` + adkEventAfterClause(after) + `
				ORDER BY timestamp DESC, ordinal DESC
				LIMIT ?
			)
			ORDER BY timestamp ASC, ordinal ASC`
		args = []any{key.appName, key.userID, key.sessionID}
		if !after.IsZero() {
			args = append(args, after.UTC().Format(adkSessionTimeFormat))
		}
		args = append(args, limit)
	} else {
		query += ` ORDER BY timestamp ASC, ordinal ASC`
	}

	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fetch adk events for %q: %w", key.sessionID, err)
	}
	defer func() { _ = rows.Close() }()

	events := make([]*adksession.Event, 0)
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, fmt.Errorf("scan adk event: %w", err)
		}
		var event adksession.Event
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, fmt.Errorf("decode adk event: %w", err)
		}
		events = append(events, &event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate adk events: %w", err)
	}
	return events, nil
}

func adkEventAfterClause(after time.Time) string {
	if after.IsZero() {
		return ""
	}
	return " AND timestamp >= ?"
}

func nextADKEventOrdinal(ctx context.Context, q dbQueryer, key adkSessionKey) (int64, error) {
	var next sql.NullInt64
	err := q.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(ordinal), 0) + 1
		FROM relay_adk_events
		WHERE app_name = ? AND user_id = ? AND session_id = ?`,
		key.appName, key.userID, key.sessionID,
	).Scan(&next)
	if err != nil {
		return 0, fmt.Errorf("next adk event ordinal: %w", err)
	}
	if !next.Valid {
		return 1, nil
	}
	return next.Int64, nil
}

func splitADKStateDeltas(delta map[string]any) (map[string]any, map[string]any, map[string]any) {
	appState := make(map[string]any)
	userState := make(map[string]any)
	sessionState := make(map[string]any)
	for key, value := range delta {
		switch {
		case strings.HasPrefix(key, adksession.KeyPrefixApp):
			appState[strings.TrimPrefix(key, adksession.KeyPrefixApp)] = value
		case strings.HasPrefix(key, adksession.KeyPrefixUser):
			userState[strings.TrimPrefix(key, adksession.KeyPrefixUser)] = value
		case strings.HasPrefix(key, adksession.KeyPrefixTemp):
			continue
		default:
			sessionState[key] = value
		}
	}
	return appState, userState, sessionState
}

func mergeADKStates(appState, userState, sessionState map[string]any) map[string]any {
	out := cloneStateMap(sessionState)
	for key, value := range appState {
		out[adksession.KeyPrefixApp+key] = value
	}
	for key, value := range userState {
		out[adksession.KeyPrefixUser+key] = value
	}
	return out
}

func filterTempState(event *adksession.Event) {
	if event == nil || len(event.Actions.StateDelta) == 0 {
		return
	}
	filtered := make(map[string]any, len(event.Actions.StateDelta))
	for key, value := range event.Actions.StateDelta {
		if strings.HasPrefix(key, adksession.KeyPrefixTemp) {
			continue
		}
		filtered[key] = value
	}
	event.Actions.StateDelta = filtered
}

func encodeStateMap(state map[string]any) (string, error) {
	if state == nil {
		state = map[string]any{}
	}
	data, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeStateMap(raw string) (map[string]any, error) {
	if strings.TrimSpace(raw) == "" {
		return map[string]any{}, nil
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return nil, err
	}
	if out == nil {
		return map[string]any{}, nil
	}
	return out, nil
}

func parseADKTime(raw string) (time.Time, error) {
	if strings.TrimSpace(raw) == "" {
		return time.Time{}, nil
	}
	return time.Parse(adkSessionTimeFormat, raw)
}

func cloneStateMap(in map[string]any) map[string]any {
	if in == nil {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	maps.Copy(out, in)
	return out
}

func cloneADKEvents(in []*adksession.Event) []*adksession.Event {
	if in == nil {
		return nil
	}
	out := make([]*adksession.Event, len(in))
	copy(out, in)
	return out
}

type dbQueryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func rollbackTx(tx *sql.Tx) {
	if tx != nil {
		_ = tx.Rollback()
	}
}
