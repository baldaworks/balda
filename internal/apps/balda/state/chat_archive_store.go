package state

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/google/uuid"
)

type sqlChatArchiveStore struct{ users *sqlUserStore }

func archiveError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, sql.ErrNoRows) {
		return chatarchivecmd.ErrNotFound
	}
	return chatarchivecmd.ErrUnavailable
}

func archiveTime(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000000000Z") }

func validArchiveSession(s chatarchivecmd.Session) bool {
	_, err := uuid.Parse(s.ID)
	return err == nil && s.StableID != "" && s.RuntimeID != "" && s.Location.Channel != "" && s.Location.AddressKey != "" && s.Location.Locator != "" && !s.StartedAt.IsZero() && (s.Persistence == chatarchivecmd.PersistenceDurable || s.Persistence == chatarchivecmd.PersistenceMemory) && s.BootID != ""
}

func (s *sqlChatArchiveStore) Begin(ctx context.Context, candidate chatarchivecmd.Intent) (chatarchivecmd.Intent, error) {
	if candidate.Token == "" || candidate.At.IsZero() || !validArchiveSession(candidate.Session) {
		return chatarchivecmd.Intent{}, chatarchivecmd.ErrInvalid
	}
	switch candidate.Kind {
	case chatarchivecmd.KindFresh, chatarchivecmd.KindExact, chatarchivecmd.KindAdopt, chatarchivecmd.KindDestroy:
	default:
		return chatarchivecmd.Intent{}, chatarchivecmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return chatarchivecmd.Intent{}, archiveError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	var raw string
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_lifecycle_intents WHERE token = ?`), candidate.Token).Scan(&raw)
	if err == nil {
		var existing chatarchivecmd.Intent
		if json.Unmarshal([]byte(raw), &existing) != nil {
			return existing, chatarchivecmd.ErrUnavailable
		}
		// Compare immutable intent data, ignoring progress written after reservation.
		compare := existing
		compare.Outcome = ""
		compare.CompletedAt = time.Time{}
		compare.BindingFingerprint = ""
		candidate.Outcome = ""
		candidate.CompletedAt = time.Time{}
		candidate.BindingFingerprint = ""
		a, _ := json.Marshal(compare)
		b, _ := json.Marshal(candidate)
		if string(a) != string(b) {
			return existing, chatarchivecmd.ErrConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return chatarchivecmd.Intent{}, archiveError(ctx, err)
	}
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT token FROM balda_archive_lifecycle_intents WHERE channel_type = ? AND address_key = ? AND outcome IN ('pending','uncertain')`), candidate.Session.Location.Channel, candidate.Session.Location.AddressKey).Scan(&raw)
	if err == nil {
		return chatarchivecmd.Intent{}, chatarchivecmd.ErrConflict
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return chatarchivecmd.Intent{}, archiveError(ctx, err)
	}
	if candidate.Kind == chatarchivecmd.KindExact || candidate.Kind == chatarchivecmd.KindDestroy {
		id := candidate.Session.ID
		if candidate.Kind == chatarchivecmd.KindDestroy {
			id = candidate.OldID
		}
		rec, err := s.readSession(ctx, tx, id)
		if err != nil {
			return chatarchivecmd.Intent{}, err
		}
		if rec.Status != chatarchivecmd.StatusActive || !sameArchiveIdentity(rec, candidate.Session) {
			return chatarchivecmd.Intent{}, chatarchivecmd.ErrConflict
		}
	}
	candidate.Outcome = chatarchivecmd.StatusPending
	body, _ := json.Marshal(candidate)
	_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_archive_lifecycle_intents(token,channel_type,address_key,outcome,record_json) VALUES(?,?,?,?,?)`), candidate.Token, candidate.Session.Location.Channel, candidate.Session.Location.AddressKey, candidate.Outcome, string(body))
	if err != nil {
		return chatarchivecmd.Intent{}, archiveError(ctx, err)
	}
	if candidate.Kind == chatarchivecmd.KindFresh || candidate.Kind == chatarchivecmd.KindAdopt {
		rec := candidate.Session
		rec.Status = chatarchivecmd.StatusPending
		rec.CoverageStart = candidate.At
		if rec.ObservedAt.IsZero() {
			rec.ObservedAt = candidate.At
		}
		rec.Partial = candidate.Kind == chatarchivecmd.KindAdopt || rec.Partial
		if err := s.writeSession(ctx, tx, rec, true); err != nil {
			return chatarchivecmd.Intent{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return chatarchivecmd.Intent{}, archiveError(ctx, err)
	}
	return candidate, nil
}

func (s *sqlChatArchiveStore) readIntent(ctx context.Context, tx *sql.Tx, token string) (chatarchivecmd.Intent, error) {
	var raw string
	var intent chatarchivecmd.Intent
	err := tx.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_lifecycle_intents WHERE token = ?`), token).Scan(&raw)
	if err != nil {
		return intent, archiveError(ctx, err)
	}
	if json.Unmarshal([]byte(raw), &intent) != nil {
		return intent, chatarchivecmd.ErrUnavailable
	}
	return intent, nil
}

func (s *sqlChatArchiveStore) writeIntent(ctx context.Context, tx *sql.Tx, i chatarchivecmd.Intent) error {
	raw, _ := json.Marshal(i)
	_, err := tx.ExecContext(ctx, s.users.bind(`UPDATE balda_archive_lifecycle_intents SET outcome = ?, record_json = ? WHERE token = ?`), i.Outcome, string(raw), i.Token)
	if err != nil {
		return archiveError(ctx, err)
	}
	return nil
}

func (s *sqlChatArchiveStore) readSession(ctx context.Context, tx *sql.Tx, id string) (chatarchivecmd.Session, error) {
	var raw string
	var rec chatarchivecmd.Session
	err := tx.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_sessions WHERE archive_id = ?`), id).Scan(&raw)
	if err != nil {
		return rec, archiveError(ctx, err)
	}
	if json.Unmarshal([]byte(raw), &rec) != nil {
		return rec, chatarchivecmd.ErrUnavailable
	}
	return rec, nil
}

func (s *sqlChatArchiveStore) writeSession(ctx context.Context, tx *sql.Tx, rec chatarchivecmd.Session, insert bool) error {
	raw, _ := json.Marshal(rec)
	var err error
	if insert {
		_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_archive_sessions(archive_id,channel_type,address_key,status,observed_at,record_json) VALUES(?,?,?,?,?,?)`), rec.ID, rec.Location.Channel, rec.Location.AddressKey, rec.Status, archiveTime(rec.ObservedAt), string(raw))
	} else {
		_, err = tx.ExecContext(ctx, s.users.bind(`UPDATE balda_archive_sessions SET status = ?, observed_at = ?, record_json = ? WHERE archive_id = ?`), rec.Status, archiveTime(rec.ObservedAt), string(raw), rec.ID)
	}
	if err != nil {
		return archiveError(ctx, err)
	}
	return nil
}

func (s *sqlChatArchiveStore) Activate(ctx context.Context, token string, live *SessionRecord) (chatarchivecmd.Session, error) {
	tx, err := s.users.begin(ctx)
	if err != nil {
		return chatarchivecmd.Session{}, archiveError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	i, err := s.readIntent(ctx, tx, token)
	if err != nil {
		return chatarchivecmd.Session{}, err
	}
	if i.Kind == chatarchivecmd.KindDestroy || (i.Outcome != chatarchivecmd.StatusPending && i.Outcome != chatarchivecmd.StatusUncertain && i.Outcome != chatarchivecmd.OutcomeCompleted) {
		return chatarchivecmd.Session{}, chatarchivecmd.ErrConflict
	}
	rec, err := s.readSession(ctx, tx, i.Session.ID)
	if err != nil {
		return rec, err
	}
	fingerprint := liveFingerprint(live, rec.ID)
	if i.Outcome == chatarchivecmd.OutcomeCompleted {
		if i.BindingFingerprint != fingerprint {
			return rec, chatarchivecmd.ErrConflict
		}
		return rec, nil
	}
	if !sameArchiveIdentity(rec, i.Session) || (i.Kind == chatarchivecmd.KindExact && rec.Status != chatarchivecmd.StatusActive && (i.Outcome != chatarchivecmd.StatusUncertain || rec.Status != chatarchivecmd.StatusUncertain)) {
		return rec, chatarchivecmd.ErrConflict
	}
	if rec.Status != chatarchivecmd.StatusPending && rec.Status != chatarchivecmd.StatusUncertain && i.Kind != chatarchivecmd.KindExact {
		return rec, chatarchivecmd.ErrConflict
	}
	rec.Status = chatarchivecmd.StatusActive
	if err := s.writeSession(ctx, tx, rec, false); err != nil {
		return rec, err
	}
	if live != nil {
		if live.SessionID != rec.StableID || live.ChannelType != rec.Location.Channel || live.AddressKey != rec.Location.AddressKey || live.AddressJSON == "" {
			return rec, chatarchivecmd.ErrInvalid
		}
		bound := *live
		bound.ArchiveID = rec.ID
		if err := s.upsertLive(ctx, tx, bound); err != nil {
			return rec, err
		}
	}
	i.BindingFingerprint = fingerprint
	i.Outcome = chatarchivecmd.OutcomeCompleted
	i.CompletedAt = time.Now().UTC()
	if err := s.writeIntent(ctx, tx, i); err != nil {
		return rec, err
	}
	if err := tx.Commit(); err != nil {
		return rec, archiveError(ctx, err)
	}
	return rec, nil
}

func (s *sqlChatArchiveStore) upsertLive(ctx context.Context, tx *sql.Tx, r SessionRecord) error {
	chat, topic := telegramAddressColumns(r)
	if r.Status == "" {
		r.Status = SessionStatusActive
	}
	_, err := tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_session_metadata(session_id,user_id,chat_id,topic_id,channel_type,address_key,address_json,agent_name,workspace_dir,branch_name,runtime_snapshot_id,archive_id,status,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(session_id) DO UPDATE SET user_id=excluded.user_id,chat_id=excluded.chat_id,topic_id=excluded.topic_id,channel_type=excluded.channel_type,address_key=excluded.address_key,address_json=excluded.address_json,agent_name=excluded.agent_name,workspace_dir=excluded.workspace_dir,branch_name=excluded.branch_name,runtime_snapshot_id=excluded.runtime_snapshot_id,archive_id=excluded.archive_id,status=excluded.status,updated_at=excluded.updated_at`), r.SessionID, r.UserID, chat, topic, r.ChannelType, r.AddressKey, r.AddressJSON, r.AgentName, r.WorkspaceDir, r.BranchName, r.RuntimeSnapshotID, r.ArchiveID, r.Status, archiveTime(time.Now()))
	if err != nil {
		return archiveError(ctx, err)
	}
	return nil
}

func (s *sqlChatArchiveStore) Complete(ctx context.Context, token, outcome string) error {
	switch outcome {
	case chatarchivecmd.OutcomeRetained, chatarchivecmd.OutcomeDestroyed, chatarchivecmd.StatusUncertain, chatarchivecmd.OutcomeAborted:
	default:
		return chatarchivecmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return archiveError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	i, err := s.readIntent(ctx, tx, token)
	if err != nil {
		return err
	}
	if i.Outcome == outcome {
		return nil
	}
	if i.Outcome != chatarchivecmd.StatusPending && i.Outcome != chatarchivecmd.StatusUncertain {
		return chatarchivecmd.ErrConflict
	}
	switch i.Kind {
	case chatarchivecmd.KindFresh, chatarchivecmd.KindAdopt:
		if outcome != chatarchivecmd.OutcomeAborted && outcome != chatarchivecmd.StatusUncertain {
			return chatarchivecmd.ErrConflict
		}
	case chatarchivecmd.KindDestroy:
		if outcome == chatarchivecmd.OutcomeAborted {
			return chatarchivecmd.ErrConflict
		}
	case chatarchivecmd.KindExact:
		// Reconciliation may prove the old runtime retained or lost without mutating it.
	default:
		return chatarchivecmd.ErrConflict
	}
	id := i.OldID
	if i.Kind != chatarchivecmd.KindDestroy {
		id = i.Session.ID
	}
	rec, err := s.readSession(ctx, tx, id)
	if err != nil {
		return err
	}
	if outcome == chatarchivecmd.OutcomeAborted && i.Kind == chatarchivecmd.KindExact && rec.Status != chatarchivecmd.StatusActive {
		return chatarchivecmd.ErrConflict
	}
	if outcome == chatarchivecmd.OutcomeAborted {
		if i.Kind != chatarchivecmd.KindExact {
			rec.Status = chatarchivecmd.StatusEnded
			rec.EndedAt = time.Now().UTC()
			rec.EndReason = "activation_failed"
		}
	}
	if outcome == chatarchivecmd.OutcomeDestroyed {
		rec.Status = chatarchivecmd.StatusEnded
		rec.EndedAt = time.Now().UTC()
		rec.EndReason = i.Reason
	}
	if outcome == chatarchivecmd.StatusUncertain {
		rec.Status = chatarchivecmd.StatusUncertain
	}
	if outcome == chatarchivecmd.OutcomeRetained {
		rec.Status = chatarchivecmd.StatusActive
	}
	if err := s.writeSession(ctx, tx, rec, false); err != nil {
		return err
	}
	i.Outcome = outcome
	i.CompletedAt = time.Now().UTC()
	if err := s.writeIntent(ctx, tx, i); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return archiveError(ctx, err)
	}
	return nil
}

func (s *sqlChatArchiveStore) Pending(ctx context.Context, channel, address string) (chatarchivecmd.Intent, error) {
	var raw string
	var i chatarchivecmd.Intent
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_lifecycle_intents WHERE channel_type = ? AND address_key = ? AND outcome IN ('pending','uncertain')`), channel, address).Scan(&raw)
	if err != nil {
		return i, archiveError(ctx, err)
	}
	if json.Unmarshal([]byte(raw), &i) != nil {
		return i, chatarchivecmd.ErrUnavailable
	}
	return i, nil
}

func (s *sqlChatArchiveStore) Get(ctx context.Context, id string) (chatarchivecmd.Session, error) {
	var raw string
	var rec chatarchivecmd.Session
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_sessions WHERE archive_id = ?`), id).Scan(&raw)
	if err != nil {
		return rec, archiveError(ctx, err)
	}
	if json.Unmarshal([]byte(raw), &rec) != nil {
		return rec, chatarchivecmd.ErrUnavailable
	}
	return rec, nil
}

func (s *sqlChatArchiveStore) List(ctx context.Context, f chatarchivecmd.Filter) ([]chatarchivecmd.Session, error) {
	if f.Limit < 1 || f.Limit > 100 || (!f.BeforeAt.IsZero() && f.BeforeID == "") {
		return nil, chatarchivecmd.ErrInvalid
	}
	q := `SELECT record_json FROM balda_archive_sessions WHERE 1=1`
	var args []any
	if f.Channel != "" {
		q += ` AND channel_type = ?`
		args = append(args, f.Channel)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if !f.BeforeAt.IsZero() {
		q += ` AND (observed_at < ? OR (observed_at = ? AND archive_id < ?))`
		args = append(args, archiveTime(f.BeforeAt), archiveTime(f.BeforeAt), f.BeforeID)
	}
	q += ` ORDER BY observed_at DESC, archive_id DESC LIMIT ?`
	args = append(args, f.Limit)
	rows, err := s.users.db.QueryContext(ctx, s.users.bind(q), args...)
	if err != nil {
		return nil, archiveError(ctx, err)
	}
	defer func() { _ = rows.Close() }()
	var out []chatarchivecmd.Session
	for rows.Next() {
		var raw string
		var rec chatarchivecmd.Session
		if err := rows.Scan(&raw); err != nil {
			return nil, archiveError(ctx, err)
		}
		if json.Unmarshal([]byte(raw), &rec) != nil {
			return nil, chatarchivecmd.ErrUnavailable
		}
		out = append(out, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, archiveError(ctx, err)
	}
	return out, nil
}

func archiveFingerprint(m chatarchivecmd.Message) string {
	m.Sequence = 0
	m.RecordedAt = time.Time{}
	raw, _ := json.Marshal(m)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (s *sqlChatArchiveStore) Record(ctx context.Context, m chatarchivecmd.Message) (chatarchivecmd.Message, error) {
	if m.Source == "" || m.SessionID == "" || m.ObservedAt.IsZero() || (m.Role != "user" && m.Role != "agent") || (m.Text == "" && len(m.Attachments) == 0) {
		return m, chatarchivecmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return m, archiveError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	fingerprint := archiveFingerprint(m)
	var raw, prior string
	err = tx.QueryRowContext(ctx, s.users.bind(`SELECT record_json,fingerprint FROM balda_archive_messages WHERE source_key = ?`), m.Source).Scan(&raw, &prior)
	if err == nil {
		if prior != fingerprint {
			return m, chatarchivecmd.ErrConflict
		}
		if json.Unmarshal([]byte(raw), &m) != nil {
			return m, chatarchivecmd.ErrUnavailable
		}
		return m, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return m, archiveError(ctx, err)
	}
	rec, err := s.readSession(ctx, tx, m.SessionID)
	if err != nil {
		return m, err
	}
	if rec.Status == chatarchivecmd.StatusPending {
		return m, chatarchivecmd.ErrConflict
	}
	err = tx.QueryRowContext(ctx, s.users.bind(`UPDATE balda_archive_sessions SET next_sequence = next_sequence + 1 WHERE archive_id = ? RETURNING next_sequence`), m.SessionID).Scan(&m.Sequence)
	if err != nil {
		return m, archiveError(ctx, err)
	}
	m.RecordedAt = time.Now().UTC()
	rawBytes, _ := json.Marshal(m)
	_, err = tx.ExecContext(ctx, s.users.bind(`INSERT INTO balda_archive_messages(archive_id,sequence,source_key,role,fingerprint,record_json) VALUES(?,?,?,?,?,?)`), m.SessionID, m.Sequence, m.Source, m.Role, fingerprint, string(rawBytes))
	if err != nil {
		return m, archiveError(ctx, err)
	}
	if m.ObservedAt.After(rec.ObservedAt) {
		rec.ObservedAt = m.ObservedAt
		if err := s.writeSession(ctx, tx, rec, false); err != nil {
			return m, err
		}
	}
	if err := tx.Commit(); err != nil {
		return m, archiveError(ctx, err)
	}
	return m, nil
}

func (s *sqlChatArchiveStore) FindMessage(ctx context.Context, source string) (chatarchivecmd.Message, error) {
	var raw string
	var m chatarchivecmd.Message
	err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_messages WHERE source_key = ?`), source).Scan(&raw)
	if err != nil {
		return m, archiveError(ctx, err)
	}
	if json.Unmarshal([]byte(raw), &m) != nil {
		return m, chatarchivecmd.ErrUnavailable
	}
	return m, nil
}

func (s *sqlChatArchiveStore) Messages(ctx context.Context, id string, before int64, limit int) ([]chatarchivecmd.Message, error) {
	if limit < 1 || limit > 100 || before < 0 {
		return nil, chatarchivecmd.ErrInvalid
	}
	q := `SELECT record_json FROM balda_archive_messages WHERE archive_id = ?`
	args := []any{id}
	if before > 0 {
		q += ` AND sequence < ?`
		args = append(args, before)
	}
	q += ` ORDER BY sequence DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.users.db.QueryContext(ctx, s.users.bind(q), args...)
	if err != nil {
		return nil, archiveError(ctx, err)
	}
	defer func() { _ = rows.Close() }()
	var out []chatarchivecmd.Message
	for rows.Next() {
		var raw string
		var m chatarchivecmd.Message
		if err := rows.Scan(&raw); err != nil {
			return nil, archiveError(ctx, err)
		}
		if json.Unmarshal([]byte(raw), &m) != nil {
			return nil, chatarchivecmd.ErrUnavailable
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, archiveError(ctx, err)
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// sameArchiveIdentity fences lifecycle operations to one existing runtime epoch.
func sameArchiveIdentity(a, b chatarchivecmd.Session) bool {
	return a.ID == b.ID && a.StableID == b.StableID && a.RuntimeID == b.RuntimeID && a.Persistence == b.Persistence && (a.Persistence != chatarchivecmd.PersistenceMemory || a.BootID == b.BootID) && a.Location.Channel == b.Location.Channel && a.Location.AddressKey == b.Location.AddressKey && a.Location.Locator == b.Location.Locator
}

func liveFingerprint(live *SessionRecord, id string) string {
	if live == nil {
		return "none"
	}
	bound := *live
	bound.ArchiveID = id
	raw, _ := json.Marshal(bound)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// ReconcileMemory ends lifetimes owned by a previous process without opening
// runtime sessions. Durable lifetimes and this process's reservations are untouched.
func (s *sqlChatArchiveStore) ReconcileMemory(ctx context.Context, bootID string) error {
	if bootID == "" {
		return chatarchivecmd.ErrInvalid
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return archiveError(ctx, err)
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `SELECT record_json FROM balda_archive_sessions WHERE status IN ('active','pending','uncertain')`)
	if err != nil {
		return archiveError(ctx, err)
	}
	var former []chatarchivecmd.Session
	for rows.Next() {
		var raw string
		var rec chatarchivecmd.Session
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return archiveError(ctx, err)
		}
		if json.Unmarshal([]byte(raw), &rec) != nil {
			_ = rows.Close()
			return chatarchivecmd.ErrUnavailable
		}
		if rec.Persistence == chatarchivecmd.PersistenceMemory && rec.BootID != bootID {
			former = append(former, rec)
		}
	}
	readErr := rows.Err()
	closeErr := rows.Close()
	if readErr != nil || closeErr != nil {
		return chatarchivecmd.ErrUnavailable
	}
	for _, rec := range former {
		rec.Status = chatarchivecmd.StatusEnded
		rec.EndedAt = time.Now().UTC()
		rec.EndReason = "interrupted"
		if err := s.writeSession(ctx, tx, rec, false); err != nil {
			return err
		}
		// Resolve the location's pending operation only if it belongs to that epoch.
		var raw string
		err := tx.QueryRowContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_lifecycle_intents WHERE channel_type=? AND address_key=? AND outcome IN ('pending','uncertain')`), rec.Location.Channel, rec.Location.AddressKey).Scan(&raw)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return archiveError(ctx, err)
		}
		var i chatarchivecmd.Intent
		if json.Unmarshal([]byte(raw), &i) != nil {
			return chatarchivecmd.ErrUnavailable
		}
		if i.Session.ID != rec.ID {
			continue
		}
		i.Outcome = chatarchivecmd.OutcomeDestroyed
		i.CompletedAt = rec.EndedAt
		if err := s.writeIntent(ctx, tx, i); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return archiveError(ctx, err)
	}
	return nil
}
