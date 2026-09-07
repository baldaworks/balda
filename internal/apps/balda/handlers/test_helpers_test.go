package handlers

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/auth"
)

const testLocatorTopicSessionID = "tg--1002667079342-8939"

type fakeOwnerKVStore struct {
	values map[string]any
}

func (s *fakeOwnerKVStore) GetJSON(_ context.Context, key string) (any, bool, error) {
	if s.values == nil {
		return nil, false, nil
	}
	val, ok := s.values[key]
	return val, ok, nil
}

func (s *fakeOwnerKVStore) SetJSON(_ context.Context, key string, value any) error {
	if s.values == nil {
		s.values = make(map[string]any)
	}
	s.values[key] = value
	return nil
}

func newOwnerStoreForTest(t *testing.T, userID int64, chatID int64) *auth.OwnerStore {
	t.Helper()

	store, err := auth.NewOwnerStore(&fakeOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := store.RegisterOwner(userID, chatID); err != nil {
		t.Fatalf("RegisterOwner() error = %v", err)
	}
	return store
}
