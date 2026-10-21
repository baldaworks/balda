// Package startupnotifyapp owns the transport-neutral startup readiness use case.
package startupnotifyapp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
	"github.com/rs/zerolog"
)

// ReadyMessage is sent to the primary administrator after all enabled transports start.
const ReadyMessage = "Boss, I'm online and ready to work."

// UserReader reads canonical users for startup recipient selection.
type UserReader interface {
	ListUsers(ctx context.Context, page usercmd.PageRequest) (usercmd.UserPage, error)
}

// DirectMessageResolver resolves one confirmed transport binding to its direct-message locator.
type DirectMessageResolver interface {
	ResolveDM(ctx context.Context, binding usercmd.Binding) (deliverycmd.Locator, error)
}

// ResolverBinding associates a resolver with one enabled channel integration.
type ResolverBinding struct {
	ChannelType string
	Resolver    DirectMessageResolver
}

// Sender sends a plain transport message without creating an agent session.
type Sender interface {
	SendPlain(ctx context.Context, locator deliverycmd.Locator, text string) error
}

// Service sends the startup readiness notification to eligible administrator bindings.
type Service struct {
	users     UserReader
	sender    Sender
	resolvers map[string]DirectMessageResolver
	logger    zerolog.Logger
}

// New constructs the startup readiness service from enabled transport resolvers.
func New(users UserReader, sender Sender, bindings []ResolverBinding, logger zerolog.Logger) (*Service, error) {
	resolvers := make(map[string]DirectMessageResolver, len(bindings))
	for _, binding := range bindings {
		channelType := normalizeChannelType(binding.ChannelType)
		if channelType == "" || binding.Resolver == nil {
			return nil, fmt.Errorf("startup notification resolver requires a channel type and implementation")
		}
		if _, exists := resolvers[channelType]; exists {
			return nil, fmt.Errorf("duplicate startup notification resolver for channel type %q", channelType)
		}
		resolvers[channelType] = binding.Resolver
	}
	return &Service{
		users: users, sender: sender, resolvers: resolvers,
		logger: logger.With().Str("component", "balda.startup_notifier").Logger(),
	}, nil
}

// Notify sends readiness messages best-effort. Recipient lookup, resolution,
// and delivery failures never make the otherwise-ready application unavailable.
func (s *Service) Notify(ctx context.Context) error {
	if s == nil || s.users == nil || s.sender == nil || len(s.resolvers) == 0 {
		return nil
	}
	admin, found, err := s.primaryAdministrator(ctx)
	if err != nil {
		s.logger.Warn().Err(err).Msg("failed to read startup notification recipient")
		return nil
	}
	if !found {
		return nil
	}

	bindings := eligibleBindings(admin.Bindings, s.resolvers)
	for _, binding := range bindings {
		resolver := s.resolvers[normalizeChannelType(binding.ChannelType)]
		locator, err := resolver.ResolveDM(ctx, binding)
		if err != nil {
			s.logger.Warn().Err(err).
				Str("channel_type", binding.ChannelType).
				Str("binding_id", binding.ID).
				Msg("failed to resolve startup notification DM")
			continue
		}
		if err := s.sender.SendPlain(ctx, locator, ReadyMessage); err != nil {
			s.logger.Warn().Err(err).
				Str("channel_type", binding.ChannelType).
				Str("binding_id", binding.ID).
				Msg("failed to send startup readiness notification")
			continue
		}
		s.logger.Info().
			Str("channel_type", binding.ChannelType).
			Str("binding_id", binding.ID).
			Msg("startup readiness notification sent")
	}
	return nil
}

func (s *Service) primaryAdministrator(ctx context.Context) (usercmd.User, bool, error) {
	var primary usercmd.User
	found := false
	request := usercmd.PageRequest{Limit: usercmd.MaxPageSize}
	for {
		page, err := s.users.ListUsers(ctx, request)
		if err != nil {
			return usercmd.User{}, false, err
		}
		for _, user := range page.Users {
			if !user.Primary || user.Role != usercmd.RoleAdministrator || user.Status != usercmd.StatusActive {
				continue
			}
			if found {
				return usercmd.User{}, false, fmt.Errorf("multiple active primary administrators")
			}
			primary, found = user, true
		}
		if page.NextAfterID == "" {
			return primary, found, nil
		}
		request.AfterID = page.NextAfterID
	}
}

func eligibleBindings(bindings []usercmd.Binding, resolvers map[string]DirectMessageResolver) []usercmd.Binding {
	eligible := make([]usercmd.Binding, 0, len(bindings))
	seen := make(map[string]bool, len(bindings))
	for _, binding := range bindings {
		channelType := normalizeChannelType(binding.ChannelType)
		principal := strings.TrimSpace(binding.Principal)
		if principal == "" || resolvers[channelType] == nil {
			continue
		}
		key := channelType + "\x00" + principal
		if seen[key] {
			continue
		}
		seen[key] = true
		binding.ChannelType = channelType
		binding.Principal = principal
		eligible = append(eligible, binding)
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].ChannelType != eligible[j].ChannelType {
			return eligible[i].ChannelType < eligible[j].ChannelType
		}
		if eligible[i].Principal != eligible[j].Principal {
			return eligible[i].Principal < eligible[j].Principal
		}
		return eligible[i].ID < eligible[j].ID
	})
	return eligible
}

func normalizeChannelType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
