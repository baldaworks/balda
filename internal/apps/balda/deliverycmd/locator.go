package deliverycmd

import (
	"fmt"
	"strings"
)

// LocatorScopeKind is the transport-neutral classification returned by a
// concrete locator codec. The exact locator ref remains the isolation key;
// this value is descriptive policy metadata only.
type LocatorScopeKind string

const (
	// LocatorScopePersonal identifies a direct/private conversation.
	LocatorScopePersonal LocatorScopeKind = "personal"
	// LocatorScopeGroup identifies a shared group, channel, stream, or topic.
	LocatorScopeGroup LocatorScopeKind = "group"
)

// Locator identifies a delivery target without exposing transport-specific tuples.
type Locator struct {
	ChannelType string `json:"channel_type"`
	AddressKey  string `json:"address_key"`
	AddressJSON string `json:"address_json"`
	SessionID   string `json:"session_id"`
}

// NewLocator builds a canonical delivery locator.
func NewLocator(channelType, addressKey, addressJSON, sessionID string) (Locator, error) {
	locator := Locator{
		ChannelType: strings.TrimSpace(channelType),
		AddressKey:  strings.TrimSpace(addressKey),
		AddressJSON: strings.TrimSpace(addressJSON),
		SessionID:   strings.TrimSpace(sessionID),
	}
	if locator.ChannelType == "" {
		return Locator{}, fmt.Errorf("channel_type is required")
	}
	if locator.AddressKey == "" {
		return Locator{}, fmt.Errorf("address_key is required")
	}
	if locator.AddressJSON == "" {
		return Locator{}, fmt.Errorf("address_json is required")
	}
	if locator.SessionID == "" {
		return Locator{}, fmt.Errorf("session_id is required")
	}
	return locator, nil
}

// DeliveryActorKey returns the canonical actor key for channel delivery work.
func (locator Locator) DeliveryActorKey() string {
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
