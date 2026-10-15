// Package destinationcmd defines transport-neutral report destination values.
package destinationcmd

import "github.com/baldaworks/balda/internal/apps/balda/deliverycmd"

const (
	TargetAlias        = "alias"
	TargetManagedAlias = "managed_alias"
	TargetLocator      = "locator"
	TargetSession      = "session"
	AliasOwner         = "owner"
)

// Target names a destination without resolving it.
type Target struct {
	Target string
	Key    string
}

// Resolved is the selected concrete locator and optional principal.
type Resolved struct {
	Locator   deliverycmd.Locator
	Principal string
}

// UserID preserves the principal accessor used by runtime consumers.
func (r Resolved) UserID() string { return r.Principal }
