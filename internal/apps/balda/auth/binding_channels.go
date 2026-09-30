package auth

import (
	"context"
	"sort"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// BindingChannels holds safe identity metadata supplied by configured transports.
type BindingChannels struct {
	mu        sync.RWMutex
	channels  map[string]usercmd.BindingChannel
	resolvers map[string]func(context.Context) (usercmd.BindingChannel, error)
}

// NewBindingChannels starts each configured channel in an identity-unavailable state.
func NewBindingChannels(channels []string) *BindingChannels {
	s := &BindingChannels{channels: make(map[string]usercmd.BindingChannel, len(channels)), resolvers: make(map[string]func(context.Context) (usercmd.BindingChannel, error))}
	for _, channel := range channels {
		s.channels[channel] = usercmd.BindingChannel{Integration: usercmd.BindingIntegration{ChannelType: channel}}
	}
	return s
}

// Register updates one configured channel after its adapter establishes identity.
func (s *BindingChannels) Register(info usercmd.BindingChannel) error {
	if err := validateBindingIntegration(info.Integration); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[info.Integration.ChannelType]; !ok {
		return usercmd.ErrForbidden
	}
	s.channels[info.Integration.ChannelType] = info
	return nil
}

// Get returns configured metadata; an empty integration key means identity is unavailable.
func (s *BindingChannels) Get(channel string) (usercmd.BindingChannel, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	info, ok := s.channels[channel]
	return info, ok
}

// List returns the configured binding interfaces in stable order.
func (s *BindingChannels) List() []usercmd.BindingChannel {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]usercmd.BindingChannel, 0, len(s.channels))
	for _, info := range s.channels {
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Integration.ChannelType < result[j].Integration.ChannelType })
	return result
}

// RegisterResolver installs a configured adapter's server-side identity lookup.
func (s *BindingChannels) RegisterResolver(channel string, resolve func(context.Context) (usercmd.BindingChannel, error)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.channels[channel]; !ok || resolve == nil {
		return usercmd.ErrForbidden
	}
	s.resolvers[channel] = resolve
	return nil
}

// Refresh retries identity discovery without involving browser-supplied identity.
func (s *BindingChannels) Refresh(ctx context.Context, channel string) (usercmd.BindingChannel, error) {
	s.mu.RLock()
	resolve := s.resolvers[channel]
	s.mu.RUnlock()
	if resolve == nil {
		return usercmd.BindingChannel{}, usercmd.ErrBindingInvitationUnavailable
	}
	info, err := resolve(ctx)
	if err != nil {
		return usercmd.BindingChannel{}, err
	}
	if info.Integration.ChannelType != channel {
		return usercmd.BindingChannel{}, usercmd.ErrBindingInvitationScope
	}
	if err := s.Register(info); err != nil {
		return usercmd.BindingChannel{}, err
	}
	return info, nil
}
