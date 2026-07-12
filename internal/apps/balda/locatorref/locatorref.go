package locatorref

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
)

const (
	channelTypeTelegram = telegramref.ChannelType
	channelTypeSlack    = "slack"
	channelTypeZulip    = "zulip"
)

// Format returns the public locator reference form used in config.
func Format(locator deliverycmd.Locator) string {
	channelType := strings.TrimSpace(locator.ChannelType)
	addressKey := strings.TrimSpace(locator.AddressKey)
	if channelType == "" || addressKey == "" {
		return ""
	}
	return channelType + ":" + addressKey
}

// Parse reconstructs a canonical session locator from a public locator ref.
func Parse(ref string) (deliverycmd.Locator, error) {
	trimmed := strings.TrimSpace(ref)
	if trimmed == "" {
		return deliverycmd.Locator{}, fmt.Errorf("locator ref is required")
	}

	channelType, rawAddressKey, ok := strings.Cut(trimmed, ":")
	if !ok {
		return deliverycmd.Locator{}, fmt.Errorf("locator ref %q must be <channel_type>:<address_key>", ref)
	}

	channelType = strings.ToLower(strings.TrimSpace(channelType))
	addressKey := strings.TrimSpace(rawAddressKey)
	if channelType == "" {
		return deliverycmd.Locator{}, fmt.Errorf("locator ref channel_type is required")
	}
	if addressKey == "" {
		return deliverycmd.Locator{}, fmt.Errorf("locator ref address_key is required")
	}

	switch channelType {
	case channelTypeTelegram:
		return telegramLocatorFromAddressKey(addressKey)
	case channelTypeZulip:
		return zulipLocatorFromAddressKey(addressKey)
	case channelTypeSlack:
		return slackLocatorFromAddressKey(addressKey)
	default:
		return deliverycmd.Locator{}, fmt.Errorf("unsupported locator transport %q", channelType)
	}
}

func telegramLocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	trimmed := strings.TrimSpace(addressKey)
	chatPart, topicPart, ok := strings.Cut(trimmed, ":")
	if !ok {
		return deliverycmd.Locator{}, fmt.Errorf("telegram address key %q must be <chat_id>:<topic_id>", addressKey)
	}
	chatID, err := strconv.ParseInt(strings.TrimSpace(chatPart), 10, 64)
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("parse telegram chat_id from %q: %w", addressKey, err)
	}
	topicID, err := strconv.Atoi(strings.TrimSpace(topicPart))
	if err != nil {
		return deliverycmd.Locator{}, fmt.Errorf("parse telegram topic_id from %q: %w", addressKey, err)
	}
	raw, _ := json.Marshal(map[string]any{"chat_id": chatID, "topic_id": topicID})
	return deliverycmd.NewLocator(
		channelTypeTelegram,
		fmt.Sprintf("%d:%d", chatID, topicID),
		string(raw),
		fmt.Sprintf("tg-%d-%d", chatID, topicID),
	)
}

func slackLocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	parts := strings.Split(strings.TrimSpace(addressKey), ":")
	switch {
	case len(parts) == 3 && parts[0] == "dm":
		if parts[1] == "" || parts[2] == "" {
			return deliverycmd.Locator{}, fmt.Errorf("slack dm address key %q must be dm:<team_id>:<channel_id>", addressKey)
		}
		return newSlackLocator(slackLocatorAddress{
			Type:    "dm",
			TeamID:  strings.TrimSpace(parts[1]),
			Channel: strings.TrimSpace(parts[2]),
		}, addressKey)
	case len(parts) == 4 && parts[0] == "t":
		if parts[1] == "" || parts[2] == "" || parts[3] == "" {
			return deliverycmd.Locator{}, fmt.Errorf("slack thread address key %q must be t:<team_id>:<channel_id>:<thread_ts>", addressKey)
		}
		return newSlackLocator(slackLocatorAddress{
			Type:     "thread",
			TeamID:   strings.TrimSpace(parts[1]),
			Channel:  strings.TrimSpace(parts[2]),
			ThreadTS: strings.TrimSpace(parts[3]),
		}, addressKey)
	default:
		return deliverycmd.Locator{}, fmt.Errorf("slack address key %q must be dm:<team_id>:<channel_id> or t:<team_id>:<channel_id>:<thread_ts>", addressKey)
	}
}

type slackLocatorAddress struct {
	Type     string `json:"type"`
	TeamID   string `json:"team_id"`
	Channel  string `json:"channel"`
	ThreadTS string `json:"thread_ts,omitempty"`
}

func newSlackLocator(address slackLocatorAddress, addressKey string) (deliverycmd.Locator, error) {
	raw, _ := json.Marshal(address)
	sum := sha256.Sum256([]byte(strings.TrimSpace(addressKey)))
	return deliverycmd.NewLocator(
		channelTypeSlack,
		strings.TrimSpace(addressKey),
		string(raw),
		fmt.Sprintf("sl-%x", sum[:8]),
	)
}

func zulipLocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	trimmed := strings.TrimSpace(addressKey)
	if strings.HasPrefix(trimmed, "s:") {
		rest := strings.TrimPrefix(trimmed, "s:")
		colonIdx := strings.Index(rest, ":")
		if colonIdx < 0 {
			return deliverycmd.Locator{}, fmt.Errorf("zulip stream address key %q must be s:<stream_id>:<topic>", addressKey)
		}
		streamIDStr := rest[:colonIdx]
		escapedTopic := rest[colonIdx+1:]
		streamID, err := strconv.Atoi(strings.TrimSpace(streamIDStr))
		if err != nil {
			return deliverycmd.Locator{}, fmt.Errorf("parse zulip stream_id from %q: %w", addressKey, err)
		}
		if streamID <= 0 {
			return deliverycmd.Locator{}, fmt.Errorf("zulip stream_id from %q must be positive", addressKey)
		}
		topic, err := url.PathUnescape(escapedTopic)
		if err != nil {
			return deliverycmd.Locator{}, fmt.Errorf("unescape zulip topic from %q: %w", addressKey, err)
		}
		raw, _ := json.Marshal(map[string]any{"type": "stream", "stream_id": streamID, "topic": topic})
		topicHash := sha256.Sum256([]byte(topic))
		return deliverycmd.NewLocator(
			channelTypeZulip,
			fmt.Sprintf("s:%d:%s", streamID, url.PathEscape(topic)),
			string(raw),
			fmt.Sprintf("zu-s-%d-%x", streamID, topicHash[:4]),
		)
	}
	if strings.HasPrefix(trimmed, "dm:") {
		rest := strings.TrimPrefix(trimmed, "dm:")
		userID, err := strconv.Atoi(strings.TrimSpace(rest))
		if err != nil {
			return deliverycmd.Locator{}, fmt.Errorf("parse zulip user_id from %q: %w", addressKey, err)
		}
		if userID <= 0 {
			return deliverycmd.Locator{}, fmt.Errorf("zulip user_id from %q must be positive", addressKey)
		}
		raw, _ := json.Marshal(map[string]any{"type": "dm", "user_id": userID})
		return deliverycmd.NewLocator(
			channelTypeZulip,
			fmt.Sprintf("dm:%d", userID),
			string(raw),
			fmt.Sprintf("zu-dm-%d", userID),
		)
	}
	return deliverycmd.Locator{}, fmt.Errorf("zulip address key %q must start with \"s:\" (stream) or \"dm:\" (direct message)", addressKey)
}
