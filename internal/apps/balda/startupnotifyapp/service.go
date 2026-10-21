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
	Enabled     bool
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
	if users == nil {
		return nil, fmt.Errorf("startup notification user reader is required")
	}
	if sender == nil {
		return nil, fmt.Errorf("startup notification sender is required")
	}
	resolvers := make(map[string]DirectMessageResolver, len(bindings))
	for _, binding := range bindings {
		if !binding.Enabled {
			continue
		}
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

// Notify sends readiness messages best-effort. Resolution and delivery
// failures never make the otherwise-ready application unavailable.
func (s *Service) Notify(ctx context.Context) error {
	if s == nil {
		return nil
	}
	admin, found, err := s.primaryAdministrator(ctx)
	if err != nil {
		return fmt.Errorf("read startup notification recipient: %w", err)
	}
	if !found {
		return nil
	}

	bindings := orderedBindings(admin.Bindings)
	for _, binding := range bindings {
		if binding.ChannelType == "" || binding.Principal == "" {
			s.logResult(binding, "skipped", "invalid_binding")
			continue
		}
		resolver := s.resolvers[binding.ChannelType]
		if resolver == nil {
			s.logResult(binding, "skipped", "integration_disabled")
			continue
		}
		locator, err := resolver.ResolveDM(ctx, binding)
		if err != nil {
			s.logResult(binding, "failed", "resolve_failed")
			continue
		}
		if err := s.sender.SendPlain(ctx, locator, ReadyMessage); err != nil {
			s.logResult(binding, "failed", "send_failed")
			continue
		}
		s.logResult(binding, "sent", "")
	}
	return nil
}

func (s *Service) logResult(binding usercmd.Binding, outcome, reason string) {
	event := s.logger.Info()
	if outcome == "failed" {
		event = s.logger.Warn()
	}
	event = event.
		Str("channel_type", binding.ChannelType).
		Str("binding_id", binding.ID).
		Str("outcome", outcome)
	if reason != "" {
		event = event.Str("reason", reason)
	}
	event.Msg("startup readiness notification result")
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

func orderedBindings(bindings []usercmd.Binding) []usercmd.Binding {
	ordered := make([]usercmd.Binding, 0, len(bindings))
	for _, binding := range bindings {
		binding.ChannelType = normalizeChannelType(binding.ChannelType)
		binding.Principal = strings.TrimSpace(binding.Principal)
		ordered = append(ordered, binding)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].ChannelType != ordered[j].ChannelType {
			return ordered[i].ChannelType < ordered[j].ChannelType
		}
		if ordered[i].Principal != ordered[j].Principal {
			return ordered[i].Principal < ordered[j].Principal
		}
		return ordered[i].ID < ordered[j].ID
	})
	return ordered
}

func normalizeChannelType(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
