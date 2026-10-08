// Package aliascmd defines transport-neutral managed destination values.
package aliascmd

import (
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

var (
	ErrInvalid     = errors.New("invalid managed alias")
	ErrForbidden   = errors.New("managed alias operation forbidden")
	ErrNotFound    = errors.New("managed alias not found")
	ErrConflict    = errors.New("managed alias conflict")
	ErrUnavailable = errors.New("managed alias operation unavailable")
)

// Authority binds a request to the current administrator and browser family.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Record maps one managed name to one canonical public locator reference.
type Record struct {
	Name       string
	LocatorRef string
	Version    uint64
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Create requests a new mapping.
type Create struct {
	Name       string
	LocatorRef string
	Authority  Authority
}

// Retarget replaces a mapping when its version is current.
type Retarget struct {
	Name            string
	LocatorRef      string
	ExpectedVersion uint64
	Authority       Authority
}

// Delete removes a mapping when its version is current.
type Delete struct {
	Name            string
	ExpectedVersion uint64
	Authority       Authority
}

// MutationKind identifies a versioned mapping write.
type MutationKind string

const (
	MutationCreate   MutationKind = "create"
	MutationRetarget MutationKind = "retarget"
	MutationDelete   MutationKind = "delete"
)

// Mutation commits a mapping and its security audit in one transaction.
type Mutation struct {
	Kind            MutationKind
	Record          Record
	ExpectedVersion uint64
	Authority       Authority
	Audit           usercmd.AuditEvent
}
