package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

// DestinationResolver resolves envelope alias targets using registered destinations,
// supporting multiple concurrent channels, role assignments, and fallback to legacy owner state.
type DestinationResolver struct {
	destStore  *DestinationStore
	ownerStore *OwnerStore
}

// NewDestinationResolver creates a new DestinationResolver.
func NewDestinationResolver(destStore *DestinationStore, ownerStore *OwnerStore) *DestinationResolver {
	return &DestinationResolver{
		destStore:  destStore,
		ownerStore: ownerStore,
	}
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

	// 1. If destination store is available, look up registered role destinations.
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

	// 2. Fallback to legacy OwnerStore if alias is "owner" and channel is either unspecified or telegram.
	if role == deliverycmd.RoleOwner && (channelFilter == "" || channelFilter == ChannelTelegram) && r.ownerStore != nil {
		owner := r.ownerStore.GetOwner()
		if owner != nil && owner.UserID != 0 && owner.ChatID != 0 {
			rawJSON, _ := json.Marshal(map[string]any{"chat_id": owner.ChatID, "topic_id": 0})
			loc, err := deliverycmd.NewLocator(
				ChannelTelegram,
				fmt.Sprintf("%d:0", owner.ChatID),
				string(rawJSON),
				fmt.Sprintf("tg-%d-0", owner.ChatID),
			)
			if err != nil {
				return envelopetarget.Resolved{}, err
			}
			return envelopetarget.Resolved{
				Locator:   loc,
				Principal: telegramref.UserID(owner.UserID),
			}, nil
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
