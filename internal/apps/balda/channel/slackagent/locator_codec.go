package slackagent

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

const (
	sessionIDPrefix = "sla"
	ChannelType     = string(deliverycmd.ChannelTypeSlackAgent)
)

type LocatorAddress struct {
	TeamID         string `json:"team_id"`
	ConversationID string `json:"conversation_id"`
	ThreadID       string `json:"thread_id,omitempty"`
}

func NewConversationLocator(teamID, conversationID string) deliverycmd.Locator {
	address := LocatorAddress{
		TeamID:         strings.TrimSpace(teamID),
		ConversationID: strings.TrimSpace(conversationID),
	}
	return newLocator(address, fmt.Sprintf("c:%s:%s", address.TeamID, address.ConversationID))
}

func NewThreadLocator(teamID, conversationID, threadID string) deliverycmd.Locator {
	address := LocatorAddress{
		TeamID:         strings.TrimSpace(teamID),
		ConversationID: strings.TrimSpace(conversationID),
		ThreadID:       strings.TrimSpace(threadID),
	}
	return newLocator(address, fmt.Sprintf("t:%s:%s:%s", address.TeamID, address.ConversationID, address.ThreadID))
}

func newLocator(address LocatorAddress, addressKey string) deliverycmd.Locator {
	raw, _ := json.Marshal(address)
	sessionID := sessionIDForAddressKey(addressKey)
	locator, err := deliverycmd.NewLocator(ChannelType, strings.TrimSpace(addressKey), string(raw), sessionID)
	if err != nil {
		return deliverycmd.Locator{
			ChannelType: ChannelType,
			AddressKey:  strings.TrimSpace(addressKey),
			AddressJSON: string(raw),
			SessionID:   sessionID,
		}
	}
	return locator
}

func sessionIDForAddressKey(addressKey string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(addressKey)))
	return fmt.Sprintf("%s-%x", sessionIDPrefix, sum[:8])
}

func DecodeLocator(locator deliverycmd.Locator) (LocatorAddress, bool, error) {
	if strings.TrimSpace(locator.ChannelType) != ChannelType {
		return LocatorAddress{}, false, nil
	}
	var address LocatorAddress
	if err := json.Unmarshal([]byte(locator.AddressJSON), &address); err != nil {
		return LocatorAddress{}, true, fmt.Errorf("decode slackagent address: %w", err)
	}
	if strings.TrimSpace(address.TeamID) == "" {
		return LocatorAddress{}, true, fmt.Errorf("slackagent locator requires team_id")
	}
	if strings.TrimSpace(address.ConversationID) == "" {
		return LocatorAddress{}, true, fmt.Errorf("slackagent locator requires conversation_id")
	}
	return address, true, nil
}

// ClassifyLocatorScope classifies a slackagent conversation from its Slack
// conversation ID. Slack DM IDs are personal even when threaded; channel and
// private-channel IDs are shared. Unknown IDs fail closed instead of risking
// cross-scope memory access.
func ClassifyLocatorScope(locator deliverycmd.Locator) (deliverycmd.LocatorScopeKind, error) {
	address, ok, err := DecodeLocator(locator)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("locator channel type %q is not slackagent", locator.ChannelType)
	}
	return classifyConversation(address.ConversationID)
}

func classifyConversation(conversationID string) (deliverycmd.LocatorScopeKind, error) {
	trimmed := strings.ToUpper(strings.TrimSpace(conversationID))
	if trimmed == "" {
		return "", fmt.Errorf("slackagent conversation id is required")
	}
	switch trimmed[0] {
	case 'D':
		return deliverycmd.LocatorScopePersonal, nil
	case 'C', 'G':
		return deliverycmd.LocatorScopeGroup, nil
	default:
		return "", fmt.Errorf("cannot classify slackagent conversation %q", conversationID)
	}
}
