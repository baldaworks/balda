// Package chatarchivecmd defines transport-neutral, immutable chat archive values.
package chatarchivecmd

import (
	"errors"
	"time"
)

// Errors distinguish invalid data, contradictory observations and storage failure.
var (
	ErrInvalid     = errors.New("invalid chat archive input")
	ErrConflict    = errors.New("chat archive conflict")
	ErrUnavailable = errors.New("chat archive unavailable")
	ErrNotFound    = errors.New("chat archive not found")
)

// Location is a historical snapshot, independent of live transport bindings.
type Location struct {
	Channel          string
	AddressKey       string
	Locator          string
	Title            string
	Audience         string
	Subdivision      string
	SubdivisionLabel string
}

// Speaker identifies the actual author at observation time.
type Speaker struct{ Principal, Name, Username string }

// Attachment contains only safe labels, never bytes, paths or download URLs.
type Attachment struct{ Name, Kind string }

// Input preserves the original human message before prompt enrichment.
type Input struct {
	Source      string
	Text        string
	Speaker     Speaker
	Attachments []Attachment
	ReceivedAt  time.Time
	Location    Location
}

// Session represents a single runtime lifetime, not a location aggregate.
type Session struct {
	ID            string
	StableID      string
	RuntimeID     string
	Persistence   string
	BootID        string
	Label         string
	Location      Location
	Status        string
	StartedAt     time.Time
	ObservedAt    time.Time
	EndedAt       time.Time
	EndReason     string
	CoverageStart time.Time
	Partial       bool
	PredecessorID string
}

// Intent journals one activation or destructive transition before runtime mutation.
type Intent struct {
	Token       string
	Kind        string
	Session     Session
	OldID       string
	Reason      string
	Outcome     string
	At          time.Time
	CompletedAt time.Time
	// BindingFingerprint freezes the ready live metadata used by activation.
	BindingFingerprint string
}

// Message is an immutable input or selected final, ordered by database sequence.
type Message struct {
	SessionID   string
	Sequence    int64
	Source      string
	Role        string
	Text        string
	Speaker     Speaker
	Attachments []Attachment
	ObservedAt  time.Time
	RecordedAt  time.Time
}

// Filter bounds the inventory by channel, status and a stable keyset cursor.
type Filter struct {
	Channel  string
	Status   string
	BeforeAt time.Time
	BeforeID string
	Limit    int
}

// Lifecycle kinds and outcomes are persisted wire values.
const (
	KindFresh          = "fresh"
	KindExact          = "exact"
	KindAdopt          = "adopt"
	KindDestroy        = "destroy"
	StatusPending      = "pending"
	StatusActive       = "active"
	StatusEnded        = "ended"
	StatusUncertain    = "uncertain"
	OutcomeCompleted   = "completed"
	OutcomeRetained    = "retained"
	OutcomeDestroyed   = "destroyed"
	OutcomeAborted     = "aborted"
	PersistenceDurable = "sqlite"
	PersistenceMemory  = "memory"
)

// Runtime state keys bind an ADK lifetime and successful activation readiness.
const (
	EpochStateKey   = "balda_archive_epoch"
	ReadyStateKey   = "balda_archive_ready"
	BindingStateKey = "balda_archive_binding"
)

// Audience and subdivision values describe independent location dimensions.
const (
	AudienceUnknown    = "unknown"
	AudiencePersonal   = "personal"
	AudienceGroup      = "group"
	SubdivisionUnknown = "unknown"
	SubdivisionRoot    = "root"
	SubdivisionTopic   = "topic"
	SubdivisionThread  = "thread"
)
