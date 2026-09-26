package mattermost

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const (
	mattermostSessionIDPrefix = "mm"
	// ChannelType is the channel type string for the Mattermost transport.
	ChannelType = "mattermost"

	// addressTypeChannel is a team channel (public or private) audience.
	addressTypeChannel = "channel"
	// addressTypeDM is the direct-message channel between the bot and one user.
	addressTypeDM = "dm"
)

// LocatorAddress is the Mattermost-specific transport address payload.
//
// Thread is stored separately from ChannelID because Mattermost models threads
// as a root post inside a channel: the locator key must stay stable for a whole
// thread so that a session maps to one conversation, not to every reply.
type LocatorAddress struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id,omitempty"`
	RootID    string `json:"root_id,omitempty"`
	TeamID    string `json:"team_id,omitempty"`
	UserID    string `json:"user_id,omitempty"`
}

// ChannelIDOf returns the Mattermost channel id encoded in a locator address.
//
// It returns an empty string when the locator does not carry a Mattermost
// address, so callers can treat a missing channel as "unknown" rather than
// failing.
func ChannelIDOf(locator deliverycmd.Locator) string {
	if strings.TrimSpace(locator.AddressJSON) == "" {
		return ""
	}
	var address LocatorAddress
	if err := json.Unmarshal([]byte(locator.AddressJSON), &address); err != nil {
		return ""
	}
	return strings.TrimSpace(address.ChannelID)
}

// NewChannelLocator builds a canonical session locator for a Mattermost channel
// (optionally scoped to one thread via rootID).
func NewChannelLocator(teamID, channelID, rootID string) deliverycmd.Locator {
	address := LocatorAddress{
		Type:      addressTypeChannel,
		TeamID:    strings.TrimSpace(teamID),
		ChannelID: strings.TrimSpace(channelID),
		RootID:    strings.TrimSpace(rootID),
	}
	return newLocator(address, channelAddressKey(address), channelSessionID(address))
}

// NewDMLocator builds a canonical session locator for a Mattermost direct
// message conversation. The channel ID of a direct channel is stable for a
// given pair of users, so it is a safe session key.
func NewDMLocator(channelID, userID string) deliverycmd.Locator {
	address := LocatorAddress{
		Type:      addressTypeDM,
		ChannelID: strings.TrimSpace(channelID),
		UserID:    strings.TrimSpace(userID),
	}
	return newLocator(address, dmAddressKey(address), dmSessionID(address))
}

func newLocator(address LocatorAddress, addressKey, sessionID string) deliverycmd.Locator {
	raw, _ := json.Marshal(address)
	channelType := string(deliverycmd.ChannelTypeMattermost)
	addressJSON := string(raw)
	locator, err := deliverycmd.NewLocator(channelType, addressKey, addressJSON, sessionID)
	if err != nil {
		return deliverycmd.Locator{
			ChannelType: channelType,
			AddressKey:  addressKey,
			AddressJSON: addressJSON,
			SessionID:   sessionID,
		}
	}
	return locator
}

func channelAddressKey(address LocatorAddress) string {
	base := fmt.Sprintf("c:%s", url.PathEscape(address.ChannelID))
	if address.RootID == "" {
		return base
	}
	return base + ":" + url.PathEscape(address.RootID)
}

func dmAddressKey(address LocatorAddress) string {
	return fmt.Sprintf("d:%s", url.PathEscape(address.ChannelID))
}

func channelSessionID(address LocatorAddress) string {
	if address.RootID == "" {
		return fmt.Sprintf("%s-c-%s", mattermostSessionIDPrefix, shortHash(address.ChannelID))
	}
	return fmt.Sprintf("%s-t-%s", mattermostSessionIDPrefix, shortHash(address.ChannelID+"|"+address.RootID))
}

func dmSessionID(address LocatorAddress) string {
	if address.UserID != "" {
		return fmt.Sprintf("%s-dm-%s", mattermostSessionIDPrefix, shortHash(address.UserID))
	}
	return fmt.Sprintf("%s-dm-%s", mattermostSessionIDPrefix, shortHash(address.ChannelID))
}

func shortHash(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:4])
}

// DecodeLocator decodes a Mattermost locator payload from canonical session
// locator fields. Returns false if the locator is not a Mattermost channel type.
func DecodeLocator(locator deliverycmd.Locator) (LocatorAddress, bool, error) {
	if strings.TrimSpace(locator.ChannelType) != string(deliverycmd.ChannelTypeMattermost) {
		return LocatorAddress{}, false, nil
	}
	var address LocatorAddress
	if err := json.Unmarshal([]byte(locator.AddressJSON), &address); err != nil {
		return LocatorAddress{}, true, fmt.Errorf("decode mattermost address: %w", err)
	}
	if err := validateLocatorAddress(address); err != nil {
		return LocatorAddress{}, true, err
	}
	return address, true, nil
}

// ClassifyLocatorScope classifies a Mattermost direct-message or channel
// locator. The exact locator key remains the isolation boundary.
func ClassifyLocatorScope(locator deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("locator channel type %q is not Mattermost", locator.ChannelType)
	}
	switch strings.TrimSpace(address.Type) {
	case addressTypeDM:
		return deliverycmd.LocatorScopePersonal, nil
	case addressTypeChannel:
		return deliverycmd.LocatorScopeGroup, nil
	default:
		return "", fmt.Errorf("unsupported Mattermost locator address type %q", address.Type)
	}
}

// LocatorFromAddressKey rebuilds a canonical Mattermost locator from an address key.
//
// Channel format: "c:<url-path-escaped channel_id>[:<url-path-escaped root_id>]"
// DM format:      "d:<url-path-escaped channel_id>".
//
// Mattermost locator address JSON carries team_id and user_id, which an address
// key alone cannot express. Those fields are recovered when the caller supplies
// them through the canonical session restore path; a key-only rebuild therefore
// produces a valid locator without the optional metadata.
func LocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	trimmed := strings.TrimSpace(addressKey)
	if strings.HasPrefix(trimmed, "c:") {
		return channelLocatorFromAddressKey(trimmed)
	}
	if strings.HasPrefix(trimmed, "d:") {
		return dmLocatorFromAddressKey(trimmed)
	}
	return deliverycmd.Locator{}, fmt.Errorf(
		"mattermost address key %q must start with \"c:\" (channel) or \"d:\" (direct message)",
		addressKey,
	)
}

func channelLocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	rest := strings.TrimPrefix(addressKey, "c:")
	channelPart, rootPart, hasRoot := strings.Cut(rest, ":")
	channelID, err := url.PathUnescape(channelPart)
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("unescape mattermost channel id from %q: %w", addressKey, err)
	}
	if strings.TrimSpace(channelID) == "" {
		return deliverycmd.Locator{}, fmt.Errorf("mattermost channel address key %q has empty channel id", addressKey)
	}
	rootID := ""
	if hasRoot {
		rootID, err = url.PathUnescape(rootPart)
		if err != nil {
			return deliverycmd.Locator{}, fmt.Errorf("unescape mattermost root id from %q: %w", addressKey, err)
		}
	}
	return NewChannelLocator("", channelID, rootID), nil
}

func dmLocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	rest := strings.TrimPrefix(addressKey, "d:")
	channelID, err := url.PathUnescape(rest)
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("unescape mattermost dm channel id from %q: %w", addressKey, err)
	}
	if strings.TrimSpace(channelID) == "" {
		return deliverycmd.Locator{}, fmt.Errorf("mattermost dm address key %q has empty channel id", addressKey)
	}
	return NewDMLocator(channelID, ""), nil
}

func validateLocatorAddress(address LocatorAddress) error {
	switch strings.TrimSpace(address.Type) {
	case addressTypeChannel, addressTypeDM:
		if strings.TrimSpace(address.ChannelID) == "" {
			return fmt.Errorf("mattermost %s locator requires a channel_id", address.Type)
		}
	default:
		return fmt.Errorf("unsupported mattermost address type %q", address.Type)
	}
	return nil
}

// ChannelIDFromLocator extracts the channel ID from a Mattermost locator.
func ChannelIDFromLocator(locator deliverycmd.Locator) (string, bool) {
	address, ok, err := DecodeLocator(locator)
	if !ok || err != nil {
		return "", false
	}
	return address.ChannelID, true
}

// RootIDFromLocator extracts the thread root post ID, if the locator is scoped
// to a thread. Returns ("", true) for a channel-level locator.
func RootIDFromLocator(locator deliverycmd.Locator) (string, bool) {
	address, ok, err := DecodeLocator(locator)
	if !ok || err != nil {
		return "", false
	}
	return address.RootID, true
}

// IsDirectLocator reports whether the locator addresses a direct conversation.
func IsDirectLocator(locator deliverycmd.Locator) bool {
	address, ok, err := DecodeLocator(locator)
	if !ok || err != nil {
		return false
	}
	return address.Type == addressTypeDM
}

// UserID returns a Mattermost transport user identifier string.
func UserID(userID string) string {
	return fmt.Sprintf("%s-%s", mattermostSessionIDPrefix, strings.TrimSpace(userID))
}

// ParseUserID decodes a canonical Mattermost transport user identifier.
func ParseUserID(value string) (string, error) {
	prefix := mattermostSessionIDPrefix + "-"
	trimmed := strings.TrimSpace(value)
	if !strings.HasPrefix(trimmed, prefix) {
		return "", fmt.Errorf("mattermost user id %q must start with %q", value, prefix)
	}
	parsed := strings.TrimPrefix(trimmed, prefix)
	if parsed == "" {
		return "", fmt.Errorf("mattermost user id %q is empty", value)
	}
	return parsed, nil
}

// ParsePostID converts a provider message ID string into an int, for the
// transport-neutral MessageID field on normalized inbound payloads. Mattermost
// post IDs are opaque 26-char strings, so the numeric field is not meaningful;
// callers should rely on ProviderMessageID for correlation.
func ParsePostID(value string) int {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil {
		return 0
	}
	return parsed
}
