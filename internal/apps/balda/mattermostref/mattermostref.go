// Package mattermostref owns the transport-neutral Mattermost locator contract.
// It is shared by public locator parsing and the concrete Mattermost adapter so
// both paths produce identical address keys and session IDs.
package mattermostref

import (
	"crypto/sha256"
	"fmt"
	"net/url"
	"strings"
)

const (
	ChannelType        = "mattermost"
	AddressTypeChannel = "channel"
	AddressTypeDM      = "dm"
	AddressTypeGroup   = "group"
)

// Address is the canonical Mattermost conversation address.
type Address struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// AddressKey returns the public address key for a Mattermost conversation.
func AddressKey(address Address) string {
	switch strings.TrimSpace(address.Type) {
	case AddressTypeChannel:
		key := "c:" + url.PathEscape(strings.TrimSpace(address.ChannelID))
		if rootID := strings.TrimSpace(address.RootID); rootID != "" {
			key += ":" + url.PathEscape(rootID)
		}
		return key
	case AddressTypeDM:
		return "d:" + url.PathEscape(strings.TrimSpace(address.ChannelID))
	case AddressTypeGroup:
		return "g:" + url.PathEscape(strings.TrimSpace(address.ChannelID))
	default:
		return ""
	}
}

// SessionID returns the stable session identity for a Mattermost address.
func SessionID(address Address) string {
	channelID := strings.TrimSpace(address.ChannelID)
	switch strings.TrimSpace(address.Type) {
	case AddressTypeChannel:
		if rootID := strings.TrimSpace(address.RootID); rootID != "" {
			return "mm-t-" + shortHash(channelID+"|"+rootID)
		}
		return "mm-c-" + shortHash(channelID)
	case AddressTypeDM:
		return "mm-dm-" + shortHash(channelID)
	case AddressTypeGroup:
		return "mm-gdm-" + shortHash(channelID)
	default:
		return ""
	}
}

// ParseAddressKey parses the public Mattermost address key.
func ParseAddressKey(addressKey string) (Address, error) {
	trimmed := strings.TrimSpace(addressKey)
	parts := strings.Split(trimmed, ":")
	var address Address
	switch {
	case len(parts) == 2 && parts[0] == "c":
		address.Type = AddressTypeChannel
		address.ChannelID = parts[1]
	case len(parts) == 3 && parts[0] == "c":
		address.Type = AddressTypeChannel
		address.ChannelID = parts[1]
		address.RootID = parts[2]
	case len(parts) == 2 && parts[0] == "d":
		address.Type = AddressTypeDM
		address.ChannelID = parts[1]
	case len(parts) == 2 && parts[0] == "g":
		address.Type = AddressTypeGroup
		address.ChannelID = parts[1]
	default:
		return Address{}, fmt.Errorf("mattermost address key %q must be c:<channel_id>, c:<channel_id>:<root_id>, d:<channel_id> or g:<channel_id>", addressKey)
	}

	var err error
	if address.ChannelID, err = unescapePart(address.ChannelID); err != nil {
		return Address{}, fmt.Errorf("parse Mattermost channel id from %q: %w", addressKey, err)
	}
	if address.RootID, err = unescapePart(address.RootID); err != nil {
		return Address{}, fmt.Errorf("parse Mattermost root id from %q: %w", addressKey, err)
	}
	if address.ChannelID == "" || (address.Type == AddressTypeChannel && len(parts) == 3 && address.RootID == "") {
		return Address{}, fmt.Errorf("mattermost address key %q contains an empty identifier", addressKey)
	}
	return address, nil
}

func unescapePart(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", nil
	}
	return url.PathUnescape(strings.TrimSpace(value))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:4])
}
