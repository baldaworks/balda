package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

const inviteTTL = 24 * time.Hour

type Invite struct {
	CreatedBy string    `json:"created_by"`
	CreatedAt time.Time `json:"created_at"`
	ExpiresAt time.Time `json:"expires_at"`
	UsedBy    string    `json:"used_by,omitempty"`
	UsedAt    time.Time `json:"used_at,omitempty"`
}

type InviteStore struct {
	store inviteKVStore
}

type inviteKVStore interface {
	GetJSON(ctx context.Context, key string) (value any, ok bool, err error)
	SetWithTTL(ctx context.Context, key string, value any, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, prefix string) ([]string, error)
}

const inviteListPrefix = "invite:"

func NewInviteStore(store inviteKVStore) (*InviteStore, error) {
	if store == nil {
		return nil, fmt.Errorf("invite store is required")
	}
	return &InviteStore{store: store}, nil
}

func (s *InviteStore) CreateInvite(ctx context.Context, createdBy string) (string, *Invite, error) {
	tokenBytes := make([]byte, 16)
	if _, err := rand.Read(tokenBytes); err != nil {
		return "", nil, fmt.Errorf("generate token: %w", err)
	}
	token := hex.EncodeToString(tokenBytes)

	now := time.Now()
	invite := Invite{
		CreatedBy: createdBy,
		CreatedAt: now,
		ExpiresAt: now.Add(inviteTTL),
	}

	key := fmt.Sprintf("%s%s", inviteListPrefix, token)
	if err := s.store.SetWithTTL(ctx, key, invite, inviteTTL); err != nil {
		return "", nil, fmt.Errorf("store invite: %w", err)
	}

	return token, &invite, nil
}

func (s *InviteStore) GetInvite(ctx context.Context, token string) (*Invite, error) {
	key := fmt.Sprintf("%s%s", inviteListPrefix, token)
	raw, ok, err := s.store.GetJSON(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get invite: %w", err)
	}
	if !ok || raw == nil {
		return nil, nil // invalid or expired (auto-deleted by KV)
	}

	data, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("marshal invite: %w", err)
	}

	var invite Invite
	if err := json.Unmarshal(data, &invite); err != nil {
		return nil, fmt.Errorf("unmarshal invite: %w", err)
	}
	if !invite.ExpiresAt.IsZero() && !invite.ExpiresAt.After(time.Now()) {
		// Best-effort cleanup for stores that have not evicted expired entries yet.
		_ = s.store.Delete(ctx, key)
		return nil, nil
	}

	// Consume: delete the invite (one-time use)
	if err := s.store.Delete(ctx, key); err != nil {
		return nil, fmt.Errorf("delete invite after consume: %w", err)
	}

	return &invite, nil
}

func (s *InviteStore) ListInvites(ctx context.Context) ([]Invite, error) {
	keys, err := s.store.List(ctx, inviteListPrefix)
	if err != nil {
		return nil, fmt.Errorf("list invite keys: %w", err)
	}

	now := time.Now()
	invites := make([]Invite, 0, len(keys))
	for _, key := range keys {
		raw, ok, err := s.store.GetJSON(ctx, key)
		if err != nil || !ok || raw == nil {
			continue
		}

		data, err := json.Marshal(raw)
		if err != nil {
			continue
		}

		var invite Invite
		if err := json.Unmarshal(data, &invite); err != nil {
			continue
		}
		if !invite.ExpiresAt.IsZero() && !invite.ExpiresAt.After(now) {
			continue
		}

		invites = append(invites, invite)
	}

	return invites, nil
}
