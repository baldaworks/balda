package state

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

func (s *sqlChatArchiveStore) CheckAuthority(ctx context.Context, a chatarchivecmd.Authority) error {
	if a.UserID == "" || a.SessionID == "" || a.UserVersion == 0 || a.CredentialVersion == 0 || a.SessionVersion == 0 || a.At.IsZero() {
		return chatarchivecmd.ErrForbidden
	}
	tx, err := s.users.begin(ctx)
	if err != nil {
		return chatarchivecmd.ErrUnavailable
	}
	defer func() { _ = tx.Rollback() }()
	authority := browserAuthority{userID: a.UserID, sessionID: a.SessionID, userVersion: a.UserVersion, credentialVersion: a.CredentialVersion, mfaVersion: a.MFAVersion, sessionVersion: a.SessionVersion, at: time.Now().UTC()}
	if _, err := s.users.checkBrowserAuthority(ctx, tx, authority); err != nil {
		if errors.Is(err, usercmd.ErrForbidden) || errors.Is(err, usercmd.ErrConflict) || errors.Is(err, usercmd.ErrNotFound) || errors.Is(err, usercmd.ErrSessionUnavailable) {
			return chatarchivecmd.ErrForbidden
		}
		return chatarchivecmd.ErrUnavailable
	}
	return nil
}

func (s *sqlChatArchiveStore) Summary(ctx context.Context, id string) (chatarchivecmd.Summary, error) {
	session, err := s.Get(ctx, id)
	if err != nil {
		return chatarchivecmd.Summary{}, err
	}
	summary := chatarchivecmd.Summary{Session: session}
	if err := s.users.db.QueryRowContext(ctx, s.users.bind(`SELECT COUNT(*) FROM balda_archive_messages WHERE archive_id = ?`), id).Scan(&summary.MessageCount); err != nil {
		return summary, archiveError(ctx, err)
	}
	if session.Location.Audience == chatarchivecmd.AudiencePersonal {
		rows, err := s.users.db.QueryContext(ctx, s.users.bind(`SELECT record_json FROM balda_archive_messages WHERE archive_id = ? AND role = 'user' ORDER BY sequence LIMIT 1`), id)
		if err != nil {
			return summary, archiveError(ctx, err)
		}
		defer func() { _ = rows.Close() }()
		if rows.Next() {
			var raw string
			var m chatarchivecmd.Message
			if err := rows.Scan(&raw); err != nil {
				return summary, archiveError(ctx, err)
			}
			if json.Unmarshal([]byte(raw), &m) != nil {
				return summary, chatarchivecmd.ErrUnavailable
			}
			summary.Speaker = m.Speaker
		}
		if err := rows.Err(); err != nil {
			return summary, archiveError(ctx, err)
		}
	}
	return summary, nil
}

func (s *sqlChatArchiveStore) Successor(ctx context.Context, id string) (string, error) {
	// Query only the stored predecessor relation; no live-location inference.
	field := s.browserJSONField("record_json", "PredecessorID")
	rows, err := s.users.db.QueryContext(ctx, s.users.bind(`SELECT archive_id FROM balda_archive_sessions WHERE `+field+` = ? ORDER BY observed_at DESC, archive_id DESC LIMIT 1`), id)
	if err != nil {
		return "", archiveError(ctx, err)
	}
	defer func() { _ = rows.Close() }()
	var next string
	if rows.Next() {
		if err := rows.Scan(&next); err != nil {
			return "", archiveError(ctx, err)
		}
	}
	if err := rows.Err(); err != nil {
		return "", archiveError(ctx, err)
	}
	return next, nil
}

// Field names are fixed source literals, never browser input.
func (s *sqlChatArchiveStore) browserJSONField(column, field string) string {
	if s.users.forUpdate != "" {
		return "(" + column + "::jsonb #>> '{" + strings.ReplaceAll(field, ".", ",") + "}')"
	}
	return "json_extract(" + column + ", '$." + field + "')"
}
