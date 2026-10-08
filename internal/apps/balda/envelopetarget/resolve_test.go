package envelopetarget

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
)

func TestResolveEnvelopeTarget_ManagedAlias(t *testing.T) {
	t.Parallel()
	resolver := &fakeResolver{resolved: map[string]Resolved{
		"main_chat": {Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1003953132277:0", SessionID: "tg--1003953132277-0"}},
	}}
	got, err := Resolve(context.Background(), resolver, Target{Target: TargetManagedAlias, Key: "main_chat"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Locator.AddressKey != "-1003953132277:0" {
		t.Fatalf("address key = %q", got.Locator.AddressKey)
	}
	_, err = Resolve(context.Background(), resolver, Target{Target: TargetManagedAlias, Key: "gone"})
	if !errors.Is(err, ErrDestinationUnavailable) {
		t.Fatalf("missing managed alias error = %v", err)
	}
}

const (
	testLocatorTopicSessionID = "tg--1002667079342-8939"
	testTelegramUserID101     = "tg-101"
)

func TestResolveEnvelopeTarget_AliasOwner(t *testing.T) {
	t.Parallel()

	tgLoc, _ := deliverycmd.NewLocator("telegram", "9001:0", `{"chat_id":9001}`, "tg-9001-0")
	resolver := &fakeResolver{
		resolved: map[string]Resolved{
			"owner": {
				Locator:   tgLoc,
				Principal: testTelegramUserID101,
			},
		},
	}

	target, err := Resolve(
		context.Background(),
		resolver,
		Target{Target: " alias ", Key: " owner "},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := target.Locator.SessionID, "tg-9001-0"; got != want {
		t.Fatalf("session_id = %q, want %q", got, want)
	}
	if got, want := target.UserID(), testTelegramUserID101; got != want {
		t.Fatalf("user_id = %q, want %q", got, want)
	}
	if got, want := target.Principal, testTelegramUserID101; got != want {
		t.Fatalf("principal = %q, want %q", got, want)
	}
}

func TestResolveEnvelopeTarget_Locator(t *testing.T) {
	t.Parallel()

	target, err := Resolve(
		context.Background(),
		nil,
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
	if got := target.UserID(); got != "" {
		t.Fatalf("user_id = %q, want empty", got)
	}
}

func TestResolveEnvelopeTarget_SlackLocator(t *testing.T) {
	t.Parallel()

	target, err := Resolve(
		context.Background(),
		nil,
		Target{Target: "locator", Key: "slackagent:c:T123:C456"},
	)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if got, want := target.Locator.ChannelType, "slackagent"; got != want {
		t.Fatalf("channel_type = %q, want %q", got, want)
	}
	if got, want := target.Locator.AddressKey, "c:T123:C456"; got != want {
		t.Fatalf("address_key = %q, want %q", got, want)
	}
}

func TestResolveEnvelopeTarget_RejectsUnknownAlias(t *testing.T) {
	t.Parallel()

	resolver := &fakeResolver{resolved: map[string]Resolved{}}
	_, err := Resolve(
		context.Background(),
		resolver,
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
		nil,
		Target{Target: "locator", Key: "telegram"},
	)
	if err == nil {
		t.Fatal("Resolve() error = nil, want non-nil")
	}
	if !strings.Contains(err.Error(), "<channel_type>:<address_key>") {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestResolveEnvelopeTarget_Validation(t *testing.T) {
	t.Parallel()

	ctx := context.Background()

	// Missing target kind
	_, err := Resolve(ctx, nil, Target{Target: "", Key: "owner"})
	if err == nil || !strings.Contains(err.Error(), "envelope target is required") {
		t.Fatalf("expected target required error, got %v", err)
	}

	// Missing key
	_, err = Resolve(ctx, nil, Target{Target: "alias", Key: ""})
	if err == nil || !strings.Contains(err.Error(), "envelope target key is required") {
		t.Fatalf("expected key required error, got %v", err)
	}

	// Missing resolver for alias
	_, err = Resolve(ctx, nil, Target{Target: "alias", Key: "owner"})
	if err == nil || !strings.Contains(err.Error(), "destination resolver is required") {
		t.Fatalf("expected destination resolver is required error, got %v", err)
	}

	// Unsupported target kind
	_, err = Resolve(ctx, nil, Target{Target: "unknown", Key: "owner"})
	if err == nil || !strings.Contains(err.Error(), "unsupported envelope target") {
		t.Fatalf("expected unsupported envelope target error, got %v", err)
	}
}

type fakeResolver struct {
	resolved map[string]Resolved
}

func (f *fakeResolver) ResolveSession(_ context.Context, sessionID string) (Resolved, error) {
	if r, ok := f.resolved[sessionID]; ok {
		return r, nil
	}
	return Resolved{}, fmt.Errorf("session %q not found", sessionID)
}

func TestResolveEnvelopeTarget_Session(t *testing.T) {
	want := Resolved{Locator: deliverycmd.Locator{SessionID: "mm-c-target", AddressKey: "c:channel:target-root"}}
	got, err := Resolve(context.Background(), &fakeResolver{resolved: map[string]Resolved{"mm-c-target": want}}, Target{Target: TargetSession, Key: "mm-c-target"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Locator != want.Locator {
		t.Fatalf("resolved locator = %+v", got.Locator)
	}
}

func (f *fakeResolver) ResolveAlias(_ context.Context, alias string) (Resolved, error) {
	norm := strings.ToLower(strings.TrimSpace(alias))
	if r, ok := f.resolved[norm]; ok {
		return r, nil
	}
	return Resolved{}, fmt.Errorf("unsupported alias target %q", alias)
}

func (f *fakeResolver) ResolveManagedAlias(_ context.Context, alias string) (Resolved, error) {
	if r, ok := f.resolved[alias]; ok {
		return r, nil
	}
	return Resolved{}, ErrDestinationUnavailable
}
