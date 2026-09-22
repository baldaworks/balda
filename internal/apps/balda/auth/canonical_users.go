package auth

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/baldaworks/balda/internal/apps/balda/users"
	"github.com/google/uuid"
)

const bindingClaimLifetime = 5 * time.Minute

type canonicalUserStore interface {
	GetUser(ctx context.Context, userID string) (usercmd.User, bool, error)
	GetUserByBinding(ctx context.Context, channelType, principal string) (usercmd.User, bool, error)
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
	CreateBindingClaim(ctx context.Context, claim usercmd.BindingClaim, createdAt time.Time, audit usercmd.AuditEvent) error
	AttachBinding(ctx context.Context, claimID string, binding usercmd.Binding, consumedAt time.Time, audit usercmd.AuditEvent) error
	UpdateUser(ctx context.Context, user usercmd.User, expectedVersion uint64, audit usercmd.AuditEvent) error
}

func canonicalSubject(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", "", fmt.Errorf("transport subject is required")
	}
	channelType := ChannelTelegram
	principal := trimmed
	if before, after, ok := strings.Cut(trimmed, ":"); ok {
		channelType = strings.ToLower(strings.TrimSpace(before))
		principal = strings.TrimSpace(after)
	}
	if principal == "" {
		return "", "", fmt.Errorf("transport principal is required")
	}
	switch channelType {
	case ChannelTelegram, ChannelZulip:
		value, err := strconv.ParseInt(principal, 10, 64)
		if err != nil || value <= 0 {
			return "", "", fmt.Errorf("invalid %s principal", channelType)
		}
		principal = strconv.FormatInt(value, 10)
	case ChannelSlack:
		teamID, userID, ok := strings.Cut(principal, ":")
		if !ok || strings.TrimSpace(teamID) == "" || strings.TrimSpace(userID) == "" {
			return "", "", fmt.Errorf("invalid Slack principal")
		}
		principal = strings.TrimSpace(teamID) + ":" + strings.TrimSpace(userID)
	default:
		return "", "", fmt.Errorf("unsupported transport %q", channelType)
	}
	return channelType, principal, nil
}

func canonicalUsers(ctx context.Context, store canonicalUserStore) ([]usercmd.User, error) {
	var result []usercmd.User
	page := usercmd.PageRequest{Limit: usercmd.MaxPageSize}
	for {
		usersPage, err := store.ListUsers(ctx, page)
		if err != nil {
			return nil, err
		}
		result = append(result, usersPage.Users...)
		if usersPage.NextAfterID == "" {
			return result, nil
		}
		page.AfterID = usersPage.NextAfterID
	}
}

func attachCanonicalBinding(
	ctx context.Context,
	store canonicalUserStore,
	role usercmd.Role,
	subject string,
	displayName string,
	provenance string,
) (bool, error) {
	channelType, principal, err := canonicalSubject(subject)
	if err != nil {
		return false, err
	}
	existing, found, err := store.GetUserByBinding(ctx, channelType, principal)
	if err != nil {
		return false, err
	}
	if found {
		if existing.Status == usercmd.StatusActive && existing.Role == role {
			return false, nil
		}
		return false, usercmd.ErrBindingPrincipalInUse
	}
	all, err := canonicalUsers(ctx, store)
	if err != nil {
		return false, err
	}
	candidates := make([]usercmd.User, 0, len(all))
	for _, candidate := range all {
		if candidate.Status == usercmd.StatusActive && candidate.Role == role && candidate.Binding == nil {
			candidates = append(candidates, candidate)
		}
	}
	if len(candidates) == 0 {
		return false, fmt.Errorf("%w: no unbound active %s user", usercmd.ErrNotFound, role)
	}
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].Primary != candidates[j].Primary {
			return candidates[i].Primary
		}
		return candidates[i].ID < candidates[j].ID
	})
	now := time.Now().UTC()
	target := candidates[0]
	claimID := uuid.NewString()
	claimAudit := canonicalAudit(usercmd.AuditActionBindingClaimCreated, usercmd.AuditTargetBinding, claimID, "binding claim created", now)
	if err := store.CreateBindingClaim(ctx, usercmd.BindingClaim{
		ID: claimID, UserID: target.ID, ChannelType: channelType, ExpiresAt: now.Add(bindingClaimLifetime),
	}, now, claimAudit); err != nil {
		return false, err
	}
	binding := usercmd.Binding{
		ID: uuid.NewString(), UserID: target.ID, ChannelType: channelType, Principal: principal,
		DisplayName: strings.TrimSpace(displayName), Provenance: strings.TrimSpace(provenance), CreatedAt: now, UpdatedAt: now,
	}
	if binding.DisplayName == "" {
		binding.DisplayName = channelType + ":" + principal
	}
	attachAudit := canonicalAudit(usercmd.AuditActionBindingAttached, usercmd.AuditTargetBinding, binding.ID, "verified bot onboarding", now)
	if err := store.AttachBinding(ctx, claimID, binding, now, attachAudit); err != nil {
		return false, err
	}
	return true, nil
}

func canonicalAudit(action usercmd.AuditAction, targetType usercmd.AuditTargetType, targetID, reason string, now time.Time) usercmd.AuditEvent {
	return usercmd.AuditEvent{
		ID: uuid.NewString(), Action: action, Outcome: usercmd.AuditOutcomeSucceeded,
		TargetType: targetType, TargetID: targetID, Reason: reason, Source: "bot-user-adapter", OccurredAt: now,
	}
}

func hasCanonicalCapability(user usercmd.User, capability usercmd.BotCapability) bool {
	return users.BotCapability(user) == capability
}
