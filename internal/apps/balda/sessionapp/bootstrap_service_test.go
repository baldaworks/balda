package sessionapp

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/telegramref"
)

type fakeTelegramOwnerActivator struct {
	calls []struct {
		ownerID int64
		chatID  int64
	}
	err error
}

func (f *fakeTelegramOwnerActivator) ActivateOwner(_ context.Context, ownerID, chatID int64) error {
	f.calls = append(f.calls, struct {
		ownerID int64
		chatID  int64
	}{ownerID: ownerID, chatID: chatID})
	return f.err
}

type fakeZulipOwnerActivator struct {
	calls []int
	err   error
}

func (f *fakeZulipOwnerActivator) ActivateOwner(_ context.Context, senderID int) error {
	f.calls = append(f.calls, senderID)
	return f.err
}

func TestBootstrapService_ActivateOwner(t *testing.T) {
	t.Run("telegram activation", func(t *testing.T) {
		tg := &fakeTelegramOwnerActivator{}
		svc := NewBootstrapService(tg, nil)
		locator := telegramref.NewLocator(9001, 0)
		if err := svc.ActivateOwner(context.Background(), locator, "101"); err != nil {
			t.Fatal(err)
		}
		if len(tg.calls) != 1 {
			t.Fatalf("telegram calls = %d, want 1", len(tg.calls))
		}
		if tg.calls[0].ownerID != 101 || tg.calls[0].chatID != 9001 {
			t.Fatalf("call = %+v, want owner=101 chat=9001", tg.calls[0])
		}
	})

	t.Run("telegram error propagates", func(t *testing.T) {
		tg := &fakeTelegramOwnerActivator{err: errors.New("boom")}
		svc := NewBootstrapService(tg, nil)
		locator := telegramref.NewLocator(9001, 0)
		if err := svc.ActivateOwner(context.Background(), locator, "101"); err == nil {
			t.Fatal("want error, got nil")
		}
	})

	t.Run("zulip activation", func(t *testing.T) {
		zu := &fakeZulipOwnerActivator{}
		svc := NewBootstrapService(nil, zu)
		locator := deliverycmd.Locator{ChannelType: "zulip", AddressKey: "dm:202", SessionID: "s1"}
		if err := svc.ActivateOwner(context.Background(), locator, "202"); err != nil {
			t.Fatal(err)
		}
		if len(zu.calls) != 1 || zu.calls[0] != 202 {
			t.Fatalf("zulip calls = %+v, want [202]", zu.calls)
		}
	})

	t.Run("unsupported transport is no-op", func(t *testing.T) {
		svc := NewBootstrapService(nil, nil)
		locator := deliverycmd.Locator{ChannelType: "unknown"}
		if err := svc.ActivateOwner(context.Background(), locator, "101"); err != nil {
			t.Fatal(err)
		}
	})
}
