package telegramref

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
)

const (
	telegramSessionIDPrefix = "tg"
	ChannelType             = "telegram"
)

// LocatorAddress is the Telegram-specific transport address payload.
type LocatorAddress struct {
	ChatID  int64 `json:"chat_id"`
	TopicID int   `json:"topic_id"`
}

// NewLocator builds a canonical session locator for Telegram transport.
func NewLocator(chatID int64, topicID int) deliverycmd.Locator {
	address := LocatorAddress{ChatID: chatID, TopicID: topicID}
	raw, _ := json.Marshal(address)
	channelType := ChannelType
	addressKey := fmt.Sprintf("%d:%d", chatID, topicID)
	addressJSON := string(raw)
	sessionID := fmt.Sprintf("%s-%d-%d", telegramSessionIDPrefix, chatID, topicID)

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

// LocatorFromAddressKey rebuilds a canonical Telegram locator from "<chat_id>:<topic_id>".
func LocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
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
	return NewLocator(chatID, topicID), nil
}

// DecodeLocator decodes a Telegram locator payload from canonical session locator fields.
func DecodeLocator(locator deliverycmd.Locator) (LocatorAddress, bool, error) {
	if strings.TrimSpace(locator.ChannelType) != ChannelType {
		return LocatorAddress{}, false, nil
	}

	var address LocatorAddress
	if err := json.Unmarshal([]byte(locator.AddressJSON), &address); err != nil {
		return LocatorAddress{}, true, fmt.Errorf("decode telegram address: %w", err)
	}
	return address, true, nil
}

// UserID returns a Telegram transport user identifier.
func UserID(userID int64) string {
	return fmt.Sprintf("%s-%d", telegramSessionIDPrefix, userID)
}
