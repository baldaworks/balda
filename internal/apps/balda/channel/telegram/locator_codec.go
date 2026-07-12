package telegram

import (
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/telegramref"
)

const (
	// ChannelType is the channel type string for the Telegram transport.
	ChannelType = telegramref.ChannelType
)

// LocatorAddress is the Telegram-specific transport address payload.
type LocatorAddress = telegramref.LocatorAddress

// NewLocator builds a canonical session locator for Telegram transport.
func NewLocator(chatID int64, topicID int) deliverycmd.Locator {
	return telegramref.NewLocator(chatID, topicID)
}

// LocatorFromAddressKey rebuilds a canonical Telegram locator from "<chat_id>:<topic_id>".
func LocatorFromAddressKey(addressKey string) (deliverycmd.Locator, error) {
	return telegramref.LocatorFromAddressKey(addressKey)
}

// DecodeLocator decodes a Telegram locator payload from canonical session locator fields.
func DecodeLocator(locator deliverycmd.Locator) (LocatorAddress, bool, error) {
	return telegramref.DecodeLocator(locator)
}

// UserID returns a Telegram transport user identifier.
func UserID(userID int64) string {
	return telegramref.UserID(userID)
}
