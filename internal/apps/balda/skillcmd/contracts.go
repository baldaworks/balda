// Package skillcmd defines transport-neutral standalone skill management values.
package skillcmd

import (
	"errors"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
)

var (
	ErrInvalid     = errors.New("invalid skill management input")
	ErrForbidden   = errors.New("skill management forbidden")
	ErrConflict    = errors.New("skills changed; reopen review")
	ErrNotFound    = errors.New("skill not found")
	ErrUnavailable = errors.New("skill management unavailable")
)

// Authority binds a request to current administrator browser authority.
type Authority struct {
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// Valid reports whether all required authority evidence is present.
func (a Authority) Valid() bool {
	return a.UserID != "" && a.UserVersion != 0 && a.CredentialVersion != 0 && a.SessionID != "" && a.SessionVersion != 0 && !a.At.IsZero()
}

// Operation identifies an accepted administrator action.
type Operation string

const (
	OperationInstall Operation = "install"
	OperationUpdate  Operation = "update"
	OperationEnable  Operation = "enable"
	OperationDisable Operation = "disable"
	OperationRemove  Operation = "remove"
	OperationRefresh Operation = "refresh"
)

// Valid reports whether an operation is supported.
func (o Operation) Valid() bool {
	switch o {
	case OperationInstall, OperationUpdate, OperationEnable, OperationDisable, OperationRemove, OperationRefresh:
		return true
	default:
		return false
	}
}

// Ownership describes which management surface owns a skill contribution.
type Ownership string

const (
	OwnershipManaged   Ownership = "managed"
	OwnershipPlugin    Ownership = "plugin"
	OwnershipUnmanaged Ownership = "unmanaged"
)

// Entry is a metadata-only browser projection. It never carries source origins.
type Entry struct {
	ID          runtimecatalogcmd.ContributionID
	Revision    runtimecatalogcmd.RevisionID
	Name        string
	Description string
	Ownership   Ownership
	SourceLabel string
	PluginID    string
	Enabled     bool
	Pending     bool
}

// Inventory combines current contributions and desired managed installations.
type Inventory struct {
	Entries       []Entry
	ExpectedState string
	Generation    uint64
	Pending       bool
}

// Candidate identifies an opaque reviewed installation without exposing its origin.
type Candidate struct {
	Handle         string
	Entry          Entry
	SkillPath      string
	ResolvedCommit string
	ExpectedState  string
	ExpiresAt      time.Time
}

// Mutation contains browser-selected identity and optimistic state evidence.
type Mutation struct {
	ID            runtimecatalogcmd.SourceID
	ExpectedState string
	Authority     Authority
}

// Outcome separates durable acceptance from catalog publication.
type Outcome struct {
	Accepted   bool
	Pending    bool
	Generation uint64
	Entry      *Entry
}
