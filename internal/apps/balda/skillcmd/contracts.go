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

type authorityKind uint8

const (
	authorityBrowser authorityKind = iota + 1
	authorityCLI
)

// Authority binds a request either to current administrator browser evidence
// or to the fixed host-local CLI authority. Its kind is intentionally closed:
// callers cannot manufacture another privileged authority variant.
type Authority struct {
	kind              authorityKind
	UserID            string
	UserVersion       uint64
	CredentialVersion uint64
	MFAVersion        uint64
	SessionID         string
	SessionVersion    uint64
	At                time.Time
}

// NewBrowserAuthority constructs administrator browser authority.
func NewBrowserAuthority(userID string, userVersion, credentialVersion, mfaVersion uint64, sessionID string, sessionVersion uint64, at time.Time) Authority {
	return Authority{kind: authorityBrowser, UserID: userID, UserVersion: userVersion, CredentialVersion: credentialVersion, MFAVersion: mfaVersion, SessionID: sessionID, SessionVersion: sessionVersion, At: at}
}

// NewCLIAuthority constructs the fixed authority used by host-local maintenance
// commands. Browser identity fields stay empty and cannot be supplied by flags.
func NewCLIAuthority(at time.Time) Authority {
	return Authority{kind: authorityCLI, At: at}
}

// Valid reports whether all required authority evidence is present.
func (a Authority) Valid() bool {
	switch a.kind {
	case authorityBrowser:
		return a.UserID != "" && a.UserVersion != 0 && a.CredentialVersion != 0 && a.SessionID != "" && a.SessionVersion != 0 && !a.At.IsZero()
	case authorityCLI:
		return a.UserID == "" && a.UserVersion == 0 && a.CredentialVersion == 0 && a.MFAVersion == 0 && a.SessionID == "" && a.SessionVersion == 0 && !a.At.IsZero()
	default:
		return false
	}
}

// Browser reports whether this authority carries browser-session evidence.
func (a Authority) Browser() bool { return a.kind == authorityBrowser && a.Valid() }

// CLI reports whether this is the fixed host-local CLI authority.
func (a Authority) CLI() bool { return a.kind == authorityCLI && a.Valid() }

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
	ID              runtimecatalogcmd.ContributionID
	CurrentRevision runtimecatalogcmd.RevisionID
	Revision        runtimecatalogcmd.RevisionID
	Name            string
	Description     string
	Ownership       Ownership
	SourceLabel     string
	PluginID        string
	Enabled         bool
	Pending         bool
}

// Inventory combines current contributions and desired managed installations.
type Inventory struct {
	Diagnostics   []string
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
