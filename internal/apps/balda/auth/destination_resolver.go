package auth

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/state"
)

// DestinationResolver resolves envelope alias targets using registered destinations.
type DestinationResolver struct {
	destStore *DestinationStore
	sessions  state.SessionStore
}

// NewDestinationResolver creates a new DestinationResolver.
func NewDestinationResolver(destStore *DestinationStore) *DestinationResolver {
	return &DestinationResolver{destStore: destStore}
}

// NewDestinationResolverWithSessions also resolves persisted session destinations.
func NewDestinationResolverWithSessions(destStore *DestinationStore, sessions state.SessionStore) *DestinationResolver {
	return &DestinationResolver{destStore: destStore, sessions: sessions}
}

// ResolveSession returns the canonical locator for an active session.
func (r *DestinationResolver) ResolveSession(ctx context.Context, sessionID string) (envelopetarget.Resolved, error) {
	if r.sessions == nil {
		return envelopetarget.Resolved{}, fmt.Errorf("session store is unavailable")
	}
	record, found, err := r.sessions.GetBySessionID(ctx, sessionID)
	if err != nil {
		return envelopetarget.Resolved{}, fmt.Errorf("read session %q: %w", sessionID, err)
	}
	if !found || (record.Status != "" && record.Status != state.SessionStatusActive) {
		return envelopetarget.Resolved{}, fmt.Errorf("active session %q not found", sessionID)
	}
	locator, err := deliverycmd.NewLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
	if err != nil {
		return envelopetarget.Resolved{}, fmt.Errorf("decode session %q locator: %w", sessionID, err)
	}
	return envelopetarget.Resolved{Locator: locator, Principal: record.UserID}, nil
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
			return envelopetarget.Resolved{}, fmt.Errorf("lookup destinations for role %q: %w", role, err)
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
