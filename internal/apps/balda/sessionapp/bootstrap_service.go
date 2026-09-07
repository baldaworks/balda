package sessionapp

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

// TelegramOwnerActivator is the minimal port needed to activate an owner on Telegram.
type TelegramOwnerActivator interface {
	ActivateOwner(ctx context.Context, ownerID, chatID int64) error
}

// ZulipOwnerActivator is the minimal port needed to activate an owner on Zulip.
type ZulipOwnerActivator interface {
	ActivateOwner(ctx context.Context, senderID int) error
}

// BootstrapService coordinates transport-specific owner activation and session bootstrapping.
type BootstrapService struct {
	telegram TelegramOwnerActivator
	zulip    ZulipOwnerActivator
}

// NewBootstrapService creates a new BootstrapService.
func NewBootstrapService(telegram TelegramOwnerActivator, zulip ZulipOwnerActivator) *BootstrapService {
	return &BootstrapService{telegram: telegram, zulip: zulip}
}

// ActivateOwner activates owner session for the specified transport origin.
func (s *BootstrapService) ActivateOwner(ctx context.Context, locator deliverycmd.Locator, principal string) error {
	switch locator.ChannelType {
	case telegramref.ChannelType:
		if s == nil || s.telegram == nil {
			return nil
		}
		address, ok, err := telegramref.DecodeLocator(locator)
		if err != nil || !ok {
			return fmt.Errorf("invalid telegram locator: %w", err)
		}
		ownerID, err := strconv.ParseInt(strings.TrimSpace(principal), 10, 64)
		if err != nil {
			return fmt.Errorf("invalid telegram owner id %q: %w", principal, err)
		}
		return s.telegram.ActivateOwner(ctx, ownerID, address.ChatID)
	case string(deliverycmd.ChannelTypeZulip):
		if s == nil || s.zulip == nil {
			return nil
		}
		senderID, err := strconv.Atoi(strings.TrimSpace(principal))
		if err != nil {
			return fmt.Errorf("invalid zulip sender id %q: %w", principal, err)
		}
		return s.zulip.ActivateOwner(ctx, senderID)
	default:
		return nil
	}
}
