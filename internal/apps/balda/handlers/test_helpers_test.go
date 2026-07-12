package handlers

import (
	"testing"

	"github.com/normahq/balda/internal/apps/balda/auth"
)

const testLocatorTopicSessionID = "tg--1002667079342-8939"

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
