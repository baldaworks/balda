package session

import (
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
)

type SessionLocator = deliverycmd.Locator

// NewSessionLocator builds a canonical session locator.
func NewSessionLocator(channelType, addressKey, addressJSON, sessionID string) (SessionLocator, error) {
	return deliverycmd.NewLocator(channelType, addressKey, addressJSON, sessionID)
}

// LocatorFromRecord reconstructs a session locator from persisted metadata.
func LocatorFromRecord(record baldastate.SessionRecord) (SessionLocator, error) {
	return NewSessionLocator(record.ChannelType, record.AddressKey, record.AddressJSON, record.SessionID)
}
