package envelopetarget

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/destinationcmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
)

// ErrSessionUnavailable means a session ID has no active destination.
var ErrSessionUnavailable = errors.New("session destination unavailable")

// ErrResolutionUnavailable means the destination backend could not be read.
var ErrResolutionUnavailable = errors.New("destination resolution unavailable")

// ErrDestinationUnavailable means a named destination has no current mapping.
var ErrDestinationUnavailable = errors.New("destination unavailable")

const (
	TargetAlias        = destinationcmd.TargetAlias
	TargetManagedAlias = destinationcmd.TargetManagedAlias
	AliasOwner         = destinationcmd.AliasOwner
	TargetLocator      = destinationcmd.TargetLocator
	TargetSession      = destinationcmd.TargetSession
)

// Target describes an envelope destination reference (either alias or locator).
type Target = destinationcmd.Target

// Resolved represents the transport-neutral resolution of an envelope target.
type Resolved = destinationcmd.Resolved

// DestinationResolver resolves an alias to a canonical delivery locator and principal.
type DestinationResolver interface {
	ResolveAlias(ctx context.Context, alias string) (Resolved, error)
}

// ManagedAliasResolver resolves an administrator-managed name independently of role aliases.
type ManagedAliasResolver interface {
	ResolveManagedAlias(ctx context.Context, name string) (Resolved, error)
}

// SessionDestinationResolver resolves an existing session ID to its persisted locator.
type SessionDestinationResolver interface {
	ResolveSession(ctx context.Context, sessionID string) (Resolved, error)
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
	case TargetManagedAlias:
		managed, ok := resolver.(ManagedAliasResolver)
		if !ok {
			return Resolved{}, fmt.Errorf("%w: managed alias resolver is required", ErrResolutionUnavailable)
		}
		return managed.ResolveManagedAlias(ctx, key)
	case TargetAlias:
		if resolver == nil {
			return Resolved{}, fmt.Errorf("%w: destination resolver is required", ErrResolutionUnavailable)
		}
		return resolver.ResolveAlias(ctx, key)
	case TargetLocator:
		locator, err := locatorref.Parse(target.Key)
		if err != nil {
			return Resolved{}, err
		}
		return Resolved{Locator: locator}, nil
	case TargetSession:
		sessionResolver, ok := resolver.(SessionDestinationResolver)
		if !ok {
			return Resolved{}, fmt.Errorf("%w: session destination resolver is required", ErrResolutionUnavailable)
		}
		return sessionResolver.ResolveSession(ctx, key)
	default:
		return Resolved{}, fmt.Errorf("unsupported envelope target %q", target.Target)
	}
}
