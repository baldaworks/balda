package auth

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

type telegramProfileStore interface {
	UpdateTelegramBindingProfile(ctx context.Context, principal, username, firstName string, updatedAt time.Time) (bool, error)
}

// TelegramProfileService keeps provider profile fields separate from canonical identity.
type TelegramProfileService struct {
	store telegramProfileStore
	now   func() time.Time
}

// NewTelegramProfileService creates a profile updater for verified Telegram events.
func NewTelegramProfileService(store telegramProfileStore) *TelegramProfileService {
	return &TelegramProfileService{store: store, now: time.Now}
}

// Refresh records the latest verified profile for an already-bound Telegram principal.
func (s *TelegramProfileService) Refresh(ctx context.Context, userID int64, username, firstName string) error {
	if s == nil || s.store == nil || userID <= 0 {
		return nil
	}
	_, err := s.store.UpdateTelegramBindingProfile(ctx, strconv.FormatInt(userID, 10),
		strings.TrimSpace(username), strings.TrimSpace(firstName), s.now().UTC())
	if err != nil {
		return fmt.Errorf("refresh telegram binding profile: %w", err)
	}
	return nil
}
