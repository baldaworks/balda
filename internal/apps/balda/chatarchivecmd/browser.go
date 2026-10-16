package chatarchivecmd

import (
	"errors"
	"time"
)

// ErrForbidden rejects stale or non-administrator browser authority.
var ErrForbidden = errors.New("chat archive access forbidden")

// Authority identifies the current authenticated browser family and user versions.
type Authority struct {
	UserID, SessionID                                          string
	UserVersion, CredentialVersion, MFAVersion, SessionVersion uint64
	At                                                         time.Time
}

// Summary adds SQL-derived counts and a personal-chat speaker to a lifetime.
type Summary struct {
	Session      Session
	MessageCount int64
	Speaker      Speaker
}

// BrowseQuery carries metadata filters and a bounded inventory cursor.
type BrowseQuery struct{ Query, Channel, Status, Cursor, ListCursor string }

// Inventory contains the newest bounded window of session summaries.
type Inventory struct {
	Rows  []Summary
	Older string
}

// Transcript contains only the selected window; private replay data is never projected.
type Transcript struct {
	Summary            Summary
	Messages           []Message
	Older, SuccessorID string
}
