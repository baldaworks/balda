package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/aliascmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
)

// SessionLookup reads a persisted session destination without coupling auth to storage.
type SessionLookup func(ctx context.Context, sessionID string) (envelopetarget.Resolved, bool, error)

// DestinationResolver resolves envelope alias targets using registered destinations.
type DestinationResolver struct {
	destStore *DestinationStore
	sessions  SessionLookup
	managed   ManagedAliasLookup
}

// ManagedAliasLookup resolves one managed name to its current concrete locator.
type ManagedAliasLookup func(ctx context.Context, name string) (deliverycmd.Locator, error)

// NewDestinationResolver creates a new DestinationResolver.
func NewDestinationResolver(destStore *DestinationStore) *DestinationResolver {
	return &DestinationResolver{destStore: destStore}
}

// NewDestinationResolverWithSessions also resolves persisted session destinations.
func NewDestinationResolverWithSessions(destStore *DestinationStore, sessions SessionLookup) *DestinationResolver {
	return &DestinationResolver{destStore: destStore, sessions: sessions}
}

// NewDestinationResolverWithManagedAliases composes role, session and managed-name resolution.
func NewDestinationResolverWithManagedAliases(destStore *DestinationStore, sessions SessionLookup, managed ManagedAliasLookup) *DestinationResolver {
	return &DestinationResolver{destStore: destStore, sessions: sessions, managed: managed}
}

// ResolveManagedAlias resolves a managed name without falling back to role aliases.
func (r *DestinationResolver) ResolveManagedAlias(ctx context.Context, name string) (envelopetarget.Resolved, error) {
	if r.managed == nil {
		return envelopetarget.Resolved{}, envelopetarget.ErrResolutionUnavailable
	}
	locator, err := r.managed(ctx, name)
	if errors.Is(err, aliascmd.ErrNotFound) || errors.Is(err, aliascmd.ErrInvalid) {
		return envelopetarget.Resolved{}, envelopetarget.ErrDestinationUnavailable
	}
	if err != nil {
		return envelopetarget.Resolved{}, fmt.Errorf("%w: managed alias lookup: %w", envelopetarget.ErrResolutionUnavailable, err)
	}
	return envelopetarget.Resolved{Locator: locator}, nil
}

// ResolveSession returns the canonical locator for an active session.
func (r *DestinationResolver) ResolveSession(ctx context.Context, sessionID string) (envelopetarget.Resolved, error) {
	if r.sessions == nil {
		return envelopetarget.Resolved{}, fmt.Errorf("%w: session store is unavailable", envelopetarget.ErrResolutionUnavailable)
	}
	resolved, found, err := r.sessions(ctx, sessionID)
	if err != nil {
		return envelopetarget.Resolved{}, fmt.Errorf("%w: read session %q: %w", envelopetarget.ErrResolutionUnavailable, sessionID, err)
	}
	if !found {
		return envelopetarget.Resolved{}, fmt.Errorf("%w: active session %q not found", envelopetarget.ErrSessionUnavailable, sessionID)
	}
	return resolved, nil
}

// ResolveAlias resolves an alias (e.g. "owner", "owner@slackagent", "collaborator") to a canonical delivery locator and principal.
func (r *DestinationResolver) ResolveAlias(ctx context.Context, alias string) (envelopetarget.Resolved, error) {
	trimmed := strings.TrimSpace(alias)
	if trimmed == "" {
		return envelopetarget.Resolved{}, fmt.Errorf("alias is required")
	}

	role, channelFilter := parseAlias(trimmed)
	if role == "" {
		return envelopetarget.Resolved{}, fmt.Errorf("unsupported alias target %q", alias)
	}

	if r.destStore != nil {
		candidates, err := r.destStore.GetDestinationsByRole(ctx, role)
		if err != nil {
			return envelopetarget.Resolved{}, fmt.Errorf("%w: lookup destinations for role %q: %w", envelopetarget.ErrResolutionUnavailable, role, err)
		}

		if channelFilter != "" {
			filtered := make([]deliverycmd.DestinationRecord, 0, len(candidates))
			for _, c := range candidates {
				if strings.EqualFold(c.ChannelType, channelFilter) {
					filtered = append(filtered, c)
				}
			}
			candidates = filtered
		}

		if len(candidates) == 1 {
			return envelopetarget.Resolved{
				Locator:   candidates[0].Locator,
				Principal: candidates[0].Principal,
			}, nil
		}

		if len(candidates) > 1 {
			var defaultCandidates []deliverycmd.DestinationRecord
			for _, c := range candidates {
				if c.IsDefault {
					defaultCandidates = append(defaultCandidates, c)
				}
			}
			if len(defaultCandidates) == 1 {
				return envelopetarget.Resolved{
					Locator:   defaultCandidates[0].Locator,
					Principal: defaultCandidates[0].Principal,
				}, nil
			}

			refs := make([]string, 0, len(candidates))
			for _, c := range candidates {
				refs = append(refs, c.LocatorRef())
			}
			return envelopetarget.Resolved{}, &deliverycmd.AmbiguousDestinationError{
				Alias:      alias,
				Candidates: refs,
			}
		}
	}

	return envelopetarget.Resolved{}, fmt.Errorf("destination not found for alias %q: %w", alias, deliverycmd.ErrDestinationNotFound)
}

func parseAlias(alias string) (role string, channel string) {
	alias = strings.TrimSpace(alias)
	if before, after, ok := strings.Cut(alias, "@"); ok {
		return strings.ToLower(strings.TrimSpace(before)), strings.ToLower(strings.TrimSpace(after))
	}
	if before, after, ok := strings.Cut(alias, ":"); ok {
		return strings.ToLower(strings.TrimSpace(before)), strings.ToLower(strings.TrimSpace(after))
	}
	return strings.ToLower(alias), ""
}
