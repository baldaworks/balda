package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
	"github.com/baldaworks/balda/internal/apps/balda/usercmd"
)

// Owner represents the authenticated admin user.
type Owner struct {
	UserID       int64     `json:"user_id"`
	ChatID       int64     `json:"chat_id,omitempty"`
	Subject      string    `json:"subject,omitempty"`
	Bindings     []string  `json:"bindings,omitempty"`
	RegisteredAt time.Time `json:"registered_at"`
}

// OwnerStore manages owner persistence.
type OwnerStore struct {
	store     ownerKVStore
	canonical canonicalUserStore
	mu        sync.RWMutex
	owner     *Owner
}

type ownerKVStore interface {
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	SetJSON(ctx context.Context, key string, value any) error
}

const ownerKVKey = "owner"

// NewOwnerStore creates a new owner store backed by key-value state.
func NewOwnerStore(stateStore ownerKVStore) (*OwnerStore, error) {
	if stateStore == nil {
		return nil, fmt.Errorf("owner state store is required")
	}
	store := &OwnerStore{
		store: stateStore,
	}

	// Try to load existing owner.
	if err := store.load(); err != nil {
		return nil, fmt.Errorf("loading owner: %w", err)
	}

	return store, nil
}

// NewCanonicalOwnerStore creates the transitional owner adapter backed only by canonical users.
func NewCanonicalOwnerStore(store canonicalUserStore) (*OwnerStore, error) {
	if store == nil {
		return nil, fmt.Errorf("canonical user store is required")
	}
	return &OwnerStore{canonical: store}, nil
}

// RegisterOwner registers a new owner if none exists.
// Returns true if registered, false if already exists.
func (s *OwnerStore) RegisterOwner(userID, chatID int64) (bool, error) {
	subject := ""
	if userID != 0 {
		subject = TelegramSubject(userID)
	}
	return s.registerOwner(userID, chatID, subject)
}

// RegisterOwnerSubject registers a non-numeric transport owner subject.
func (s *OwnerStore) RegisterOwnerSubject(subject string) (bool, error) {
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return false, fmt.Errorf("owner subject is required")
	}
	return s.registerOwner(0, 0, trimmed)
}

func (s *OwnerStore) registerOwner(userID, chatID int64, subject string) (bool, error) {
	if s.canonical != nil {
		provenance := "verified-owner-onboarding"
		if chatID != 0 {
			provenance += ";chat_id=" + strconv.FormatInt(chatID, 10)
		}
		return attachCanonicalBinding(context.Background(), s.canonical, usercmd.RoleAdministrator, subject, subject, provenance)
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.owner != nil {
		return false, nil
	}

	bindings := make([]string, 0, 1)
	trimmedSubject := strings.TrimSpace(subject)
	if trimmedSubject != "" {
		bindings = append(bindings, trimmedSubject)
	}
	s.owner = &Owner{
		UserID:       userID,
		ChatID:       chatID,
		Subject:      trimmedSubject,
		Bindings:     bindings,
		RegisteredAt: time.Now(),
	}

	if err := s.saveLocked(); err != nil {
		return false, fmt.Errorf("saving owner: %w", err)
	}

	return true, nil
}

// IsOwner checks if the given user ID is the registered owner.
func (s *OwnerStore) IsOwner(userID int64) bool {
	if s.canonical != nil {
		return s.IsOwnerSubject(TelegramSubject(userID))
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.owner == nil {
		return false
	}
	if userID == 0 {
		return false
	}
	if s.owner.UserID != 0 && s.owner.UserID == userID {
		return true
	}
	return isOwnerSubject(s.owner, TelegramSubject(userID))
}

// BindOwnerSubject attaches a verified subject to an available administrator.
func (s *OwnerStore) BindOwnerSubject(subject string) error {
	if s.canonical != nil {
		_, err := attachCanonicalBinding(context.Background(), s.canonical, usercmd.RoleAdministrator, subject, subject, "verified-owner-binding")
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.owner == nil {
		return fmt.Errorf("no owner registered")
	}
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return fmt.Errorf("owner subject is required")
	}
	if isOwnerSubject(s.owner, trimmed) {
		return nil
	}
	s.owner.Bindings = append(s.owner.Bindings, trimmed)
	if strings.TrimSpace(s.owner.Subject) == "" {
		s.owner.Subject = trimmed
	}
	return s.saveLocked()
}

// BindOwnerTelegram attaches a verified Telegram subject to an available administrator.
func (s *OwnerStore) BindOwnerTelegram(userID, chatID int64) error {
	if s.canonical != nil {
		provenance := "verified-owner-binding"
		if chatID != 0 {
			provenance += ";chat_id=" + strconv.FormatInt(chatID, 10)
		}
		_, err := attachCanonicalBinding(context.Background(), s.canonical, usercmd.RoleAdministrator, TelegramSubject(userID), TelegramSubject(userID), provenance)
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if userID == 0 {
		return fmt.Errorf("owner user ID is required")
	}
	if s.owner == nil {
		return fmt.Errorf("no owner registered")
	}
	if s.owner.UserID == 0 {
		s.owner.UserID = userID
	}
	if chatID != 0 {
		s.owner.ChatID = chatID
	}
	if !isOwnerSubject(s.owner, TelegramSubject(userID)) {
		s.owner.Bindings = append(s.owner.Bindings, TelegramSubject(userID))
	}
	return s.saveLocked()
}

// OwnerSubjects returns all known channel-qualified owner subjects.
func (s *OwnerStore) OwnerSubjects() []string {
	if s.canonical != nil {
		all, err := canonicalUsers(context.Background(), s.canonical)
		if err != nil {
			return nil
		}
		var subjects []string
		for _, user := range all {
			if hasCanonicalCapability(user, usercmd.BotCapabilityOwner) {
				subjects = append(subjects, user.Binding.ChannelType+":"+user.Binding.Principal)
			}
		}
		sort.Strings(subjects)
		return subjects
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.owner == nil {
		return nil
	}
	out := make([]string, 0, len(s.owner.Bindings)+2)
	seen := map[string]struct{}{}
	add := func(subject string) {
		trimmed := strings.TrimSpace(subject)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	add(s.owner.Subject)
	if s.owner.UserID != 0 {
		add(TelegramSubject(s.owner.UserID))
	}
	for _, subject := range s.owner.Bindings {
		add(subject)
	}
	return out
}

// HasOwnerSubject reports whether any subject is bound for the given channel.
func (s *OwnerStore) HasOwnerSubject(channel string) bool {
	prefix := strings.TrimSpace(channel) + ":"
	for _, subject := range s.OwnerSubjects() {
		if strings.HasPrefix(subject, prefix) {
			return true
		}
	}
	return false
}

// IsOwnerSubject checks if the given transport subject is the registered owner.
func (s *OwnerStore) IsOwnerSubject(subject string) bool {
	if s.canonical != nil {
		channelType, principal, err := canonicalSubject(subject)
		if err != nil {
			return false
		}
		user, found, err := s.canonical.GetUserByBinding(context.Background(), channelType, principal)
		return err == nil && found && hasCanonicalCapability(user, usercmd.BotCapabilityOwner)
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return isOwnerSubject(s.owner, subject)
}

func isOwnerSubject(owner *Owner, subject string) bool {
	if owner == nil {
		return false
	}
	trimmed := strings.TrimSpace(subject)
	if trimmed == "" {
		return false
	}
	if strings.TrimSpace(owner.Subject) != "" && owner.Subject == trimmed {
		return true
	}
	for _, binding := range owner.Bindings {
		if strings.TrimSpace(binding) == trimmed {
			return true
		}
	}
	return false
}

// TelegramSubject returns the channel-qualified subject for a Telegram user.
func TelegramSubject(userID int64) string {
	return fmt.Sprintf("telegram:%d", userID)
}

// SlackSubject returns the channel-qualified subject for a Slack user.
func SlackSubject(teamID, userID string) string {
	return fmt.Sprintf("slackagent:%s:%s", strings.TrimSpace(teamID), strings.TrimSpace(userID))
}

// ZulipSubject returns the channel-qualified subject for a Zulip user.
func ZulipSubject(userID int) string {
	return fmt.Sprintf("zulip:%d", userID)
}

// UpdateChatID updates and persists the owner's chat ID.
func (s *OwnerStore) UpdateChatID(chatID int64) error {
	if s.canonical != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.owner == nil {
		return fmt.Errorf("no owner registered")
	}
	s.owner.ChatID = chatID
	return s.saveLocked()
}

// GetOwner returns the registered owner, or nil if none exists.
func (s *OwnerStore) GetOwner() *Owner {
	if s.canonical != nil {
		all, err := canonicalUsers(context.Background(), s.canonical)
		if err != nil {
			return nil
		}
		owners := make([]usercmd.User, 0, len(all))
		for _, user := range all {
			if hasCanonicalCapability(user, usercmd.BotCapabilityOwner) {
				owners = append(owners, user)
			}
		}
		sort.Slice(owners, func(i, j int) bool {
			if owners[i].Primary != owners[j].Primary {
				return owners[i].Primary
			}
			return owners[i].ID < owners[j].ID
		})
		if len(owners) != 0 {
			user := owners[0]
			subject := user.Binding.ChannelType + ":" + user.Binding.Principal
			owner := &Owner{Subject: subject, Bindings: []string{subject}, RegisteredAt: user.CreatedAt}
			if user.Binding.ChannelType == ChannelTelegram {
				owner.UserID, _ = strconv.ParseInt(user.Binding.Principal, 10, 64)
				owner.ChatID = provenanceInt64(user.Binding.Provenance, "chat_id")
			}
			return owner
		}
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return cloneOwner(s.owner)
}

func provenanceInt64(provenance, key string) int64 {
	prefix := strings.TrimSpace(key) + "="
	for _, field := range strings.Split(provenance, ";") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(field), prefix); ok {
			parsed, _ := strconv.ParseInt(value, 10, 64)
			return parsed
		}
	}
	return 0
}

// HasOwner returns true if an owner is registered.
func (s *OwnerStore) HasOwner() bool {
	if s.canonical != nil {
		return len(s.OwnerSubjects()) != 0
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	return s.owner != nil
}

// ResolveAlias resolves an alias directly against owner state as a fallback DestinationResolver.
func (s *OwnerStore) ResolveAlias(_ context.Context, alias string) (envelopetarget.Resolved, error) {
	if s.canonical != nil {
		return envelopetarget.Resolved{}, fmt.Errorf("owner alias requires a registered destination")
	}
	trimmed := strings.ToLower(strings.TrimSpace(alias))
	if trimmed != "owner" {
		return envelopetarget.Resolved{}, fmt.Errorf("unsupported alias target %q", alias)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.owner == nil {
		return envelopetarget.Resolved{}, fmt.Errorf("owner is not registered")
	}
	if s.owner.UserID == 0 {
		return envelopetarget.Resolved{}, fmt.Errorf("owner.user_id is required")
	}
	if s.owner.ChatID == 0 {
		return envelopetarget.Resolved{}, fmt.Errorf("owner.chat_id is required")
	}

	rawJSON, _ := json.Marshal(map[string]any{"chat_id": s.owner.ChatID, "topic_id": 0})
	loc, err := deliverycmd.NewLocator(
		"telegram",
		fmt.Sprintf("%d:0", s.owner.ChatID),
		string(rawJSON),
		fmt.Sprintf("tg-%d-0", s.owner.ChatID),
	)
	if err != nil {
		return envelopetarget.Resolved{}, err
	}
	return envelopetarget.Resolved{
		Locator:   loc,
		Principal: telegramref.UserID(s.owner.UserID),
	}, nil
}

func (s *OwnerStore) load() error {
	raw, ok, err := s.store.GetJSON(context.Background(), ownerKVKey)
	if err != nil {
		return fmt.Errorf("get owner state: %w", err)
	}
	if !ok || raw == nil {
		return nil
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshal owner state: %w", err)
	}
	var owner Owner
	if err := json.Unmarshal(data, &owner); err != nil {
		return fmt.Errorf("unmarshalling owner: %w", err)
	}
	owner.Bindings = normalizeOwnerBindings(owner)

	s.mu.Lock()
	s.owner = &owner
	s.mu.Unlock()
	return nil
}

func (s *OwnerStore) saveLocked() error {
	if err := s.store.SetJSON(context.Background(), ownerKVKey, cloneOwner(s.owner)); err != nil {
		return fmt.Errorf("set owner state: %w", err)
	}

	return nil
}

func cloneOwner(owner *Owner) *Owner {
	if owner == nil {
		return nil
	}
	out := *owner
	out.Bindings = append([]string(nil), owner.Bindings...)
	return &out
}

func normalizeOwnerBindings(owner Owner) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(owner.Bindings)+2)
	add := func(subject string) {
		trimmed := strings.TrimSpace(subject)
		if trimmed == "" {
			return
		}
		if _, ok := seen[trimmed]; ok {
			return
		}
		seen[trimmed] = struct{}{}
		out = append(out, trimmed)
	}
	add(owner.Subject)
	if owner.UserID != 0 {
		add(TelegramSubject(owner.UserID))
	}
	for _, subject := range owner.Bindings {
		add(subject)
	}
	return out
}
