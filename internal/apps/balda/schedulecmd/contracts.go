// Package schedulecmd defines transport-neutral schedule management values.
package schedulecmd

import (
	"errors"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid schedule")
	ErrForbidden   = errors.New("schedule management forbidden")
	ErrNotFound    = errors.New("schedule not found")
	ErrConflict    = errors.New("schedule conflict")
	ErrUnavailable = errors.New("schedule operation unavailable")
)

// Authority binds a management request to the current administrator and browser family.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Target identifies a supported envelope destination in its original form.
type Target struct {
	Kind string
	Key  string
}

// Definition is the editable portion of a recurring schedule.
type Definition struct {
	ID       string
	Cron     string
	Target   Target
	Content  string
	ReportTo *Target
}

// Item is the safe management read model. Content is shown only on guarded detail.
type Item struct {
	Definition Definition
	Source     string
	Enabled    bool
	Deleted    bool
	Version    uint64
	Status     string
	NextRunAt  time.Time
	LastRunAt  time.Time
}

type Create struct {
	Definition Definition
	Authority  Authority
}

type Update struct {
	ID              string
	ExpectedVersion uint64
	Definition      Definition
	Authority       Authority
}

type ChangeSelection struct {
	ID              string
	ExpectedVersion uint64
	Enabled         bool
	Authority       Authority
}

type Delete struct {
	ID              string
	ExpectedVersion uint64
	Authority       Authority
}

// RunNow requests one idempotent execution without changing recurrence.
type RunNow struct {
	ID              string
	RequestKey      string
	ConfirmDisabled bool
	Authority       Authority
}

// RunItem is a bounded, content-free view of one execution.
type RunItem struct {
	ID              string
	Trigger         string
	RequestedAt     time.Time
	DueAt           time.Time
	State           string
	CompletedAt     time.Time
	SafeFailureCode string
}
