package session

import (
	"fmt"
	"strings"

	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

// SessionLocator identifies a balda session without exposing channel-specific
// tuple parameters through the manager API.
type SessionLocator struct {
	ChannelType string
	AddressKey  string
	AddressJSON string
	SessionID   string
}

// NewSessionLocator builds a canonical session locator.
func NewSessionLocator(channelType, addressKey, addressJSON, sessionID string) (SessionLocator, error) {
	locator := SessionLocator{
		ChannelType: strings.TrimSpace(channelType),
		AddressKey:  strings.TrimSpace(addressKey),
		AddressJSON: strings.TrimSpace(addressJSON),
		SessionID:   strings.TrimSpace(sessionID),
	}
	if locator.ChannelType == "" {
		return SessionLocator{}, fmt.Errorf("channel_type is required")
	}
	if locator.AddressKey == "" {
		return SessionLocator{}, fmt.Errorf("address_key is required")
	}
	if locator.AddressJSON == "" {
		return SessionLocator{}, fmt.Errorf("address_json is required")
	}
	if locator.SessionID == "" {
		return SessionLocator{}, fmt.Errorf("session_id is required")
	}
	return locator, nil
}

// LocatorFromRecord reconstructs a session locator from persisted metadata.
func LocatorFromRecord(record baldastate.SessionRecord) (SessionLocator, error) {
	return NewSessionLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
}

// DeliveryActorKey returns the canonical actor key for channel delivery work.
// The normal form is "<channel_type>:<address_key>" so runtime lanes, logs,
// and command telemetry stay namespaced by transport.
func (locator SessionLocator) DeliveryActorKey() string {
	channelType := strings.ToLower(strings.TrimSpace(locator.ChannelType))
	addressKey := strings.TrimSpace(locator.AddressKey)
	if channelType != "" && addressKey != "" {
		return channelType + ":" + addressKey
	}
	for _, candidate := range []string{addressKey, strings.TrimSpace(locator.SessionID), channelType, "telegram"} {
		if candidate != "" {
			return candidate
		}
	}
	return ""
}
