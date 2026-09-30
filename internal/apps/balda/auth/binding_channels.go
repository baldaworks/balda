package auth

import (
	"sort"
	"sync"

	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// BindingChannels holds safe identity metadata supplied by configured transports.
type BindingChannels struct {
	mu       sync.RWMutex
	channels map[string]usercmd.BindingChannel
}

// NewBindingChannels starts each configured channel in an identity-unavailable state.
func NewBindingChannels(channels []string) *BindingChannels {
	s := &BindingChannels{channels: make(map[string]usercmd.BindingChannel, len(channels))}
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
