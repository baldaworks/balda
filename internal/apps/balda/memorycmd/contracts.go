// Package memorycmd defines application-global fact-memory values.
package memorycmd

import (
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// Key identifies the existing application-global memory record.
const Key = "memory/global"

var (
	ErrInvalid     = errors.New("invalid fact")
	ErrDisabled    = errors.New("fact memory is disabled")
	ErrForbidden   = errors.New("fact memory operation forbidden")
	ErrNotFound    = errors.New("fact not found")
	ErrConflict    = errors.New("fact memory changed")
	ErrUnavailable = errors.New("fact memory unavailable")
)

// Record is the persisted global memory boundary. Its JSON shape is unchanged.
type Record struct {
	Version   int64   `json:"version"`
	UpdatedAt string  `json:"updated_at"`
	Entries   []Entry `json:"entries"`
}

// Entry retains its creation version as identity across edits.
type Entry struct {
	Version   int64  `json:"version"`
	CreatedAt string `json:"created_at"`
	Fact      string `json:"fact"`
}

// Fact projects one current entry without exposing persistence details.
type Fact struct {
	ID        int64
	Fact      string
	CreatedAt string
}

// Inventory is a consistent view of the one global memory source.
type Inventory struct {
	Enabled   bool
	Version   int64
	UpdatedAt string
	Facts     []Fact
}

// Editor binds a selected fact to its loaded global version.
type Editor struct {
	Fact            Fact
	ExpectedVersion int64
}

// Authority binds a request to existing administrator browser authority.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Create appends an explicitly submitted fact.
type Create struct {
	Fact      string
	Authority Authority
}

// Edit replaces one fact only against the loaded global version.
type Edit struct {
	ID              int64
	Fact            string
	ExpectedVersion int64
	Authority       Authority
}

// Delete removes one fact only against the loaded global version.
type Delete struct {
	ID              int64
	ExpectedVersion int64
	Authority       Authority
}

// Mutation commits the current global record and its body-free audit atomically.
type Mutation struct {
	Record          Record
	ExpectedVersion int64
	Authority       Authority
	Audit           usercmd.AuditEvent
}
