package sessionapp

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/locatorref"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

// TelegramTopicChannel is the minimal port needed for Telegram forum topic management.
type TelegramTopicChannel interface {
	CreateTopicLocator(ctx context.Context, chatID int64, topicName string) (deliverycmd.Locator, error)
	Close(ctx context.Context, locator deliverycmd.Locator) error
}

// TopicService coordinates transport-specific topic locator creation and closing.
type TopicService struct {
	telegram TelegramTopicChannel
}

// NewTopicService creates a new TopicService.
func NewTopicService(telegram TelegramTopicChannel) *TopicService {
	return &TopicService{telegram: telegram}
}

// CreateTopic creates a topic locator for the specified transport origin.
func (s *TopicService) CreateTopic(ctx context.Context, locator deliverycmd.Locator, topicName string) (deliverycmd.Locator, error) {
	switch locator.ChannelType {
	case telegramref.ChannelType:
		if s == nil || s.telegram == nil {
			return deliverycmd.Locator{}, fmt.Errorf("telegram channel is unavailable")
		}
		address, ok, err := telegramref.DecodeLocator(locator)
		if err != nil || !ok {
			return deliverycmd.Locator{}, fmt.Errorf("invalid telegram locator: %w", err)
		}
		return s.telegram.CreateTopicLocator(ctx, address.ChatID, fmt.Sprintf("Balda: %s", strings.TrimSpace(topicName)))
	case string(deliverycmd.ChannelTypeZulip):
		streamID, ok := locatorref.ZulipStreamID(locator)
		if !ok {
			return deliverycmd.Locator{}, fmt.Errorf("could not determine stream ID from current context")
		}
		return locatorref.NewZulipStreamLocator(streamID, strings.TrimSpace(topicName))
	default:
		return deliverycmd.Locator{}, fmt.Errorf("topic creation is not supported for transport %q", locator.ChannelType)
	}
}

// CloseTopic closes/deletes the forum topic if supported by the transport.
func (s *TopicService) CloseTopic(ctx context.Context, locator deliverycmd.Locator) error {
	switch locator.ChannelType {
	case telegramref.ChannelType:
		if s == nil || s.telegram == nil {
			return nil
		}
		return s.telegram.Close(ctx, locator)
	case string(deliverycmd.ChannelTypeZulip):
		return nil
	default:
		return nil
	}
}

// IsTopic returns true if the locator points to an existing topic thread.
func (s *TopicService) IsTopic(locator deliverycmd.Locator) bool {
	switch locator.ChannelType {
	case telegramref.ChannelType:
		address, ok, err := telegramref.DecodeLocator(locator)
		return ok && err == nil && address.TopicID > 0
	case string(deliverycmd.ChannelTypeZulip):
		_, ok := locatorref.ZulipStreamID(locator)
		return ok
	default:
		return false
	}
}
