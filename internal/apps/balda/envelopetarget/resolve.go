package envelopetarget

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
)

const (
	TargetAlias   = "alias"
	AliasOwner    = "owner"
	TargetLocator = "locator"
)

// Target describes an envelope destination reference (either alias or locator).
type Target struct {
	Target string
	Key    string
}

// Resolved represents the transport-neutral resolution of an envelope target.
type Resolved struct {
	Locator   deliverycmd.Locator
	Principal string
}

// UserID returns the principal string for compatibility with callers expecting UserID.
func (r Resolved) UserID() string {
	return r.Principal
}

// DestinationResolver resolves an alias to a canonical delivery locator and principal.
type DestinationResolver interface {
	ResolveAlias(ctx context.Context, alias string) (Resolved, error)
}

// Resolve resolves an envelope target into a canonical delivery locator and principal.
func Resolve(
	ctx context.Context,
	resolver DestinationResolver,
	target Target,
) (Resolved, error) {
	targetKind := strings.ToLower(strings.TrimSpace(target.Target))
	key := strings.TrimSpace(target.Key)
	if targetKind == "" {
		return Resolved{}, fmt.Errorf("envelope target is required")
	}
	if key == "" {
		return Resolved{}, fmt.Errorf("envelope target key is required")
	}

	switch targetKind {
	case TargetAlias:
		if resolver == nil {
			return Resolved{}, fmt.Errorf("destination resolver is required")
		}
		return resolver.ResolveAlias(ctx, key)
	case TargetLocator:
		locator, err := locatorref.Parse(target.Key)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Locator: locator}, nil
	default:
		return Resolved{}, fmt.Errorf("unsupported envelope target %q", target.Target)
	}
}
