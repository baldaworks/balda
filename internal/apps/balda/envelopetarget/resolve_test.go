package envelopetarget

import (
	"context"
	"strings"
	"testing"

	"github.com/normahq/balda/internal/apps/balda/auth"
)

const (
	testLocatorTopicSessionID = "tg--1002667079342-8939"
	testTelegramUserID101     = "tg-101"
)

func TestResolveEnvelopeTarget_AliasOwner(t *testing.T) {
	t.Parallel()

	target, err := Resolve(
		context.Background(),
		newOwnerStoreForTest(t, 101, 9001),
		Target{Target: " alias ", Key: " owner "},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := target.Locator.SessionID, "tg-9001-0"; got != want {
		t.Fatalf("session_id = %q, want %q", got, want)
	}
	if got, want := target.UserID, testTelegramUserID101; got != want {
		t.Fatalf("user_id = %q, want %q", got, want)
	}
	if got := target.TopicID; got != 0 {
		t.Fatalf("topic_id = %d, want 0", got)
	}
}

func TestResolveEnvelopeTarget_Locator(t *testing.T) {
	t.Parallel()

	target, err := Resolve(
		context.Background(),
		newOwnerStoreForTest(t, 101, 9001),
		Target{Target: " locator ", Key: " telegram:-1002667079342:8939 "},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := target.Locator.SessionID, testLocatorTopicSessionID; got != want {
		t.Fatalf("session_id = %q, want %q", got, want)
	}
	if got, want := target.Locator.AddressKey, "-1002667079342:8939"; got != want {
		t.Fatalf("address_key = %q, want %q", got, want)
	}
	if got := target.UserID; got != "" {
		t.Fatalf("user_id = %q, want empty", got)
	}
	if got := target.TopicID; got != 8939 {
		t.Fatalf("topic_id = %d, want 8939", got)
	}
}

func TestResolveEnvelopeTarget_RejectsUnknownAlias(t *testing.T) {
	t.Parallel()

	_, err := Resolve(
		context.Background(),
		newOwnerStoreForTest(t, 101, 9001),
		Target{Target: "alias", Key: "vasya"},
	)
	if err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), `unsupported alias target "vasya"`) {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveEnvelopeTarget_RejectsInvalidLocator(t *testing.T) {
	t.Parallel()

	_, err := Resolve(
		context.Background(),
		newOwnerStoreForTest(t, 101, 9001),
		Target{Target: "locator", Key: "telegram"},
	)
	if err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "<channel_type>:<address_key>") {
		t.Fatalf("Resolve() error = %v", err)
	}
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

type fakeOwnerKVStore struct {
	value any
	ok    bool
	err   error
}

func (s *fakeOwnerKVStore) GetJSON(_ context.Context, _ string) (any, bool, error) {
	if s.err != nil {
		return nil, false, s.err
	}
	return s.value, s.ok, nil
}

func (s *fakeOwnerKVStore) SetJSON(_ context.Context, _ string, value any) error {
	if s.err != nil {
		return s.err
	}
	s.value = value
	s.ok = true
	return nil
}
