package auth

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/envelopetarget"
)

func TestDestinationResolver_SingleDestination(t *testing.T) {
	t.Parallel()

	kv := newMemoryOwnerKV()
	destStore, err := NewDestinationStore(kv)
	if err != nil {
		t.Fatalf("NewDestinationStore() error = %v", err)
	}

	slackLoc, _ := deliverycmd.NewLocator(ChannelSlack, "c:T1:C1", `{"team_id":"T1"}`, "slack-T1-C1")
	err = destStore.RegisterDestination(context.Background(), deliverycmd.DestinationRecord{
		ChannelType: ChannelSlack,
		Locator:     slackLoc,
		Roles:       []string{"owner"},
		Principal:   "slackagent:T1:U1",
	})
	if err != nil {
		t.Fatalf("RegisterDestination() error = %v", err)
	}

	resolver := NewDestinationResolver(destStore, nil)
	res, err := resolver.ResolveAlias(context.Background(), "owner")
	if err != nil {
		t.Fatalf("ResolveAlias(owner) error = %v", err)
	}

	if res.Locator.ChannelType != ChannelSlack {
		t.Fatalf("res.Locator.ChannelType = %q, want %s", res.Locator.ChannelType, ChannelSlack)
	}
	if res.Principal != "slackagent:T1:U1" {
		t.Fatalf("res.Principal = %q, want slackagent:T1:U1", res.Principal)
	}
}

func TestDestinationResolver_MultiChannel_WithDefault(t *testing.T) {
	t.Parallel()

	kv := newMemoryOwnerKV()
	destStore, err := NewDestinationStore(kv)
	if err != nil {
		t.Fatalf("NewDestinationStore() error = %v", err)
	}

	tgLoc, _ := deliverycmd.NewLocator(ChannelTelegram, "100:0", `{"chat_id":100}`, "tg-100-0")
	slackLoc, _ := deliverycmd.NewLocator(ChannelSlack, "c:T1:C1", `{"team_id":"T1"}`, "slack-T1-C1")

	ctx := context.Background()
	_ = destStore.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: ChannelTelegram,
		Locator:     tgLoc,
		Roles:       []string{"owner"},
		IsDefault:   true,
		Principal:   "telegram:100",
	})
	_ = destStore.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: ChannelSlack,
		Locator:     slackLoc,
		Roles:       []string{"owner"},
		IsDefault:   false,
		Principal:   "slackagent:T1:U1",
	})

	resolver := NewDestinationResolver(destStore, nil)
	res, err := resolver.ResolveAlias(ctx, "owner")
	if err != nil {
		t.Fatalf("ResolveAlias(owner) error = %v", err)
	}
	if res.Locator.ChannelType != ChannelTelegram {
		t.Fatalf("res.Locator.ChannelType = %q, want telegram (default)", res.Locator.ChannelType)
	}

	// Now switch default to slackagent.
	_ = destStore.SetDefaultDestination(ctx, "slackagent:c:T1:C1", "owner")
	res, err = resolver.ResolveAlias(ctx, "owner")
	if err != nil {
		t.Fatalf("ResolveAlias(owner) after switch error = %v", err)
	}
	if res.Locator.ChannelType != ChannelSlack {
		t.Fatalf("res.Locator.ChannelType = %q, want slackagent", res.Locator.ChannelType)
	}
}

func TestDestinationResolver_MultiChannel_Ambiguity(t *testing.T) {
	t.Parallel()

	kv := newMemoryOwnerKV()
	destStore, err := NewDestinationStore(kv)
	if err != nil {
		t.Fatalf("NewDestinationStore() error = %v", err)
	}

	tgLoc, _ := deliverycmd.NewLocator(ChannelTelegram, "100:0", `{"chat_id":100}`, "tg-100-0")
	slackLoc, _ := deliverycmd.NewLocator(ChannelSlack, "c:T1:C1", `{"team_id":"T1"}`, "slack-T1-C1")

	ctx := context.Background()
	_ = destStore.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: ChannelTelegram,
		Locator:     tgLoc,
		Roles:       []string{"owner"},
		IsDefault:   false,
	})
	_ = destStore.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: ChannelSlack,
		Locator:     slackLoc,
		Roles:       []string{"owner"},
		IsDefault:   false,
	})

	resolver := NewDestinationResolver(destStore, nil)
	_, err = resolver.ResolveAlias(ctx, "owner")
	if err == nil {
		t.Fatal("expected AmbiguousDestinationError, got nil")
	}
	if !errors.Is(err, deliverycmd.ErrAmbiguousDestination) {
		t.Fatalf("expected ErrAmbiguousDestination, got %v", err)
	}

	// But channel qualification should succeed unambiguously!
	resSlack, err := resolver.ResolveAlias(ctx, "owner@slackagent")
	if err != nil {
		t.Fatalf("ResolveAlias(owner@slackagent) error = %v", err)
	}
	if resSlack.Locator.ChannelType != ChannelSlack {
		t.Fatalf("resSlack.Locator.ChannelType = %q, want slackagent", resSlack.Locator.ChannelType)
	}

	resTg, err := resolver.ResolveAlias(ctx, "owner:telegram")
	if err != nil {
		t.Fatalf("ResolveAlias(owner:telegram) error = %v", err)
	}
	if resTg.Locator.ChannelType != ChannelTelegram {
		t.Fatalf("resTg.Locator.ChannelType = %q, want telegram", resTg.Locator.ChannelType)
	}
}

func TestDestinationResolver_LegacyFallback(t *testing.T) {
	t.Parallel()

	ownerStore, err := NewOwnerStore(newMemoryOwnerKV())
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	_, _ = ownerStore.RegisterOwner(202, 8080)

	destStore, _ := NewDestinationStore(newMemoryOwnerKV())

	resolver := NewDestinationResolver(destStore, ownerStore)
	res, err := resolver.ResolveAlias(context.Background(), "owner")
	if err != nil {
		t.Fatalf("ResolveAlias(owner) fallback error = %v", err)
	}
	if res.Locator.ChannelType != "telegram" {
		t.Fatalf("res.Locator.ChannelType = %q, want telegram", res.Locator.ChannelType)
	}
	if res.Locator.AddressKey != "8080:0" {
		t.Fatalf("res.Locator.AddressKey = %q, want 8080:0", res.Locator.AddressKey)
	}
	if res.Principal != "tg-202" {
		t.Fatalf("res.Principal = %q, want tg-202", res.Principal)
	}
}

func TestDestinationResolver_NotFound(t *testing.T) {
	t.Parallel()

	resolver := NewDestinationResolver(nil, nil)
	_, err := resolver.ResolveAlias(context.Background(), "owner")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !errors.Is(err, deliverycmd.ErrDestinationNotFound) {
		t.Fatalf("expected ErrDestinationNotFound, got %v", err)
	}
}

func TestResolve_WithDestinationResolver(t *testing.T) {
	t.Parallel()

	tgLoc, _ := deliverycmd.NewLocator("telegram", "555:0", `{"chat_id":555}`, "tg-555-0")
	destStore, _ := NewDestinationStore(newMemoryOwnerKV())
	_ = destStore.RegisterDestination(context.Background(), deliverycmd.DestinationRecord{
		ChannelType: "telegram",
		Locator:     tgLoc,
		Roles:       []string{"owner"},
		Principal:   "tg-555",
	})

	resolver := NewDestinationResolver(destStore, nil)
	resolved, err := envelopetarget.Resolve(context.Background(), resolver, envelopetarget.Target{
		Target: "alias",
		Key:    "owner",
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Locator.SessionID != "tg-555-0" {
		t.Fatalf("SessionID = %q, want tg-555-0", resolved.Locator.SessionID)
	}
	if resolved.UserID() != "tg-555" {
		t.Fatalf("UserID() = %q, want tg-555", resolved.UserID())
	}
}
