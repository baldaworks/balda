package auth

import (
	"context"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

func TestDestinationStore_RegisterAndGet(t *testing.T) {
	t.Parallel()

	kv := newMemoryOwnerKV()
	store, err := NewDestinationStore(kv)
	if err != nil {
		t.Fatalf("NewDestinationStore() error = %v", err)
	}

	tgLoc, _ := deliverycmd.NewLocator("telegram", "123:0", `{"chat_id":123}`, "tg-123-0")
	slackLoc, _ := deliverycmd.NewLocator("slackagent", "c:T1:C1", `{"team_id":"T1","channel_id":"C1"}`, "slack-T1-C1")

	ctx := context.Background()

	// Register Telegram destination as owner.
	err = store.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: "telegram",
		Locator:     tgLoc,
		Roles:       []string{deliverycmd.RoleOwner},
		IsDefault:   true,
		Principal:   "telegram:123",
	})
	if err != nil {
		t.Fatalf("RegisterDestination(telegram) error = %v", err)
	}

	// Register Slack destination with both owner and collaborator roles.
	err = store.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: "slackagent",
		Locator:     slackLoc,
		Roles:       []string{deliverycmd.RoleOwner, deliverycmd.RoleCollaborator},
		IsDefault:   false,
		Principal:   "slackagent:T1:U1",
	})
	if err != nil {
		t.Fatalf("RegisterDestination(slack) error = %v", err)
	}

	// Query destinations by role "owner".
	owners, err := store.GetDestinationsByRole(ctx, "owner")
	if err != nil {
		t.Fatalf("GetDestinationsByRole(owner) error = %v", err)
	}
	if len(owners) != 2 {
		t.Fatalf("len(owners) = %d, want 2", len(owners))
	}

	// Verify default owner destination is Telegram.
	defOwner, ok, err := store.GetDefaultDestinationByRole(ctx, "owner")
	if err != nil || !ok {
		t.Fatalf("GetDefaultDestinationByRole(owner) ok = %v, err = %v", ok, err)
	}
	if defOwner.LocatorRef() != "telegram:123:0" {
		t.Fatalf("defOwner.LocatorRef() = %q, want telegram:123:0", defOwner.LocatorRef())
	}

	// Switch default owner destination to Slack via SetDefaultDestination.
	err = store.SetDefaultDestination(ctx, "slackagent:c:T1:C1", "owner")
	if err != nil {
		t.Fatalf("SetDefaultDestination(slack) error = %v", err)
	}

	// Verify new default is Slack and Telegram default was cleared.
	defOwner, ok, err = store.GetDefaultDestinationByRole(ctx, "owner")
	if err != nil || !ok {
		t.Fatalf("GetDefaultDestinationByRole(owner) after switch ok = %v, err = %v", ok, err)
	}
	if defOwner.LocatorRef() != "slackagent:c:T1:C1" {
		t.Fatalf("defOwner.LocatorRef() = %q, want slackagent:c:T1:C1", defOwner.LocatorRef())
	}

	tgDest, ok, err := store.GetDestination(ctx, "telegram:123:0")
	if err != nil || !ok {
		t.Fatalf("GetDestination(telegram) ok = %v, err = %v", ok, err)
	}
	if tgDest.IsDefault {
		t.Fatalf("telegram destination IsDefault = true, want false")
	}

	// Verify reload from KV store.
	store2, err := NewDestinationStore(kv)
	if err != nil {
		t.Fatalf("NewDestinationStore(reload) error = %v", err)
	}
	list, err := store2.ListDestinations(ctx)
	if err != nil {
		t.Fatalf("ListDestinations() error = %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len(list) = %d, want 2", len(list))
	}

	// Remove destination.
	err = store2.RemoveDestination(ctx, "telegram:123:0")
	if err != nil {
		t.Fatalf("RemoveDestination() error = %v", err)
	}
	_, ok, _ = store2.GetDestination(ctx, "telegram:123:0")
	if ok {
		t.Fatalf("destination still exists after remove")
	}
}

func TestDestinationStore_Validation(t *testing.T) {
	t.Parallel()

	store, err := NewDestinationStore(newMemoryOwnerKV())
	if err != nil {
		t.Fatalf("NewDestinationStore() error = %v", err)
	}

	ctx := context.Background()

	// Invalid record: channel mismatch.
	loc, _ := deliverycmd.NewLocator("telegram", "123:0", `{"chat_id":123}`, "tg-123-0")
	err = store.RegisterDestination(ctx, deliverycmd.DestinationRecord{
		ChannelType: "slackagent",
		Locator:     loc,
	})
	if err == nil {
		t.Fatal("expected error on channel mismatch, got nil")
	}
}
