//go:build integration && (sqlite || postgres)

package state

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/chatarchivecmd"
	adksession "google.golang.org/adk/v2/session"
)

func checkChatArchive(t *testing.T, open contractOpener) {
	ctx := t.Context()
	path := filepath.Join(t.TempDir(), "archive.db")
	p, err := open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p != nil {
			_ = p.Close()
		}
	}()
	s := p.ChatArchive()
	now := time.Now().UTC()
	candidate := chatarchivecmd.Session{ID: "33c9805d-c079-4f04-a52b-77f0ce015d47", StableID: "tg-10-0", RuntimeID: "tg-10-0", Persistence: "sqlite", BootID: "boot", Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "10:0", Locator: "telegram:10:0", Audience: "personal", Subdivision: "root"}, StartedAt: now, ObservedAt: now}
	intent := chatarchivecmd.Intent{Token: "create-1", Kind: "fresh", Session: candidate, At: now}
	reserved, err := s.Begin(ctx, intent)
	if err != nil {
		t.Fatal(err)
	}
	again, err := s.Begin(ctx, reserved)
	if err != nil || again.Token != reserved.Token {
		t.Fatalf("retry reservation = %+v, %v", again, err)
	}
	conflicting := intent
	conflicting.Token = "other-operation"
	if _, err := s.Begin(ctx, conflicting); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("concurrent operation = %v", err)
	}
	live := SessionRecord{SessionID: candidate.StableID, UserID: "actual-user", ChannelType: "telegram", AddressKey: "10:0", AddressJSON: "{}", Status: "active"}
	if _, err := s.Activate(ctx, intent.Token, &live); err != nil {
		t.Fatal(err)
	}
	saved, found, err := p.Sessions().GetBySessionID(ctx, candidate.StableID)
	if err != nil || !found || saved.ArchiveID != candidate.ID {
		t.Fatalf("live archive binding = %+v,%t,%v", saved, found, err)
	}
	input := chatarchivecmd.Message{SessionID: candidate.ID, Source: "telegram:bot:message:1", Role: "user", Text: "hello", Speaker: chatarchivecmd.Speaker{Principal: "42", Name: "Anna"}, ObservedAt: now}
	first, err := s.Record(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	againMsg, err := s.Record(ctx, input)
	if err != nil || againMsg.Sequence != first.Sequence {
		t.Fatalf("dedupe = %+v,%v", againMsg, err)
	}
	changed := input
	changed.Text = "different"
	if _, err := s.Record(ctx, changed); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("immutable conflict = %v", err)
	}
	var wg sync.WaitGroup
	for n := range 24 {
		i := n % 12
		wg.Go(func() {
			m := input
			m.Source = fmt.Sprintf("message-%d", i)
			m.Text = fmt.Sprintf("text-%d", i)
			if _, err := s.Record(ctx, m); err != nil {
				t.Errorf("concurrent record: %v", err)
			}
		})
	}
	wg.Wait()
	page, err := s.Messages(ctx, candidate.ID, 0, 5)
	if err != nil || len(page) != 5 || page[0].Sequence != 9 || page[4].Sequence != 13 {
		t.Fatalf("recent page = %+v,%v", page, err)
	}
	older, err := s.Messages(ctx, candidate.ID, page[0].Sequence, 5)
	if err != nil || len(older) != 5 || older[4].Sequence != 8 {
		t.Fatalf("older page = %+v,%v", older, err)
	}
	// Deleting the live ADK history never touches the independent archive.
	if _, err := p.RuntimeSessions().Create(ctx, &adksession.CreateRequest{AppName: "archive-contract", UserID: "actual-user", SessionID: candidate.RuntimeID}); err != nil {
		t.Fatal(err)
	}
	if err := p.RuntimeSessions().Delete(ctx, &adksession.DeleteRequest{AppName: "archive-contract", UserID: "actual-user", SessionID: candidate.RuntimeID}); err != nil {
		t.Fatal(err)
	}
	if err := p.Sessions().DeleteBySessionID(ctx, candidate.StableID); err != nil {
		t.Fatal(err)
	}
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	p = nil
	p, err = open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	s = p.ChatArchive()
	restored, err := s.Get(ctx, candidate.ID)
	if err != nil || restored.Location.Locator != candidate.Location.Locator {
		t.Fatalf("archive reopen=%+v,%v", restored, err)
	}
	messages, err := s.Messages(ctx, candidate.ID, 0, 100)
	if err != nil || len(messages) != 13 || messages[0].Speaker.Name != "Anna" {
		t.Fatalf("retained transcript=%+v,%v", messages, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := s.Record(cancelled, input); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled record=%v", err)
	}
}

func checkChatArchiveLifecycle(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	ctx := t.Context()
	s := p.ChatArchive()
	now := time.Now().UTC()
	rec := chatarchivecmd.Session{ID: "279fbf42-8955-44b7-b95b-51cd4e98ac33", StableID: "session-1", RuntimeID: "runtime-1", Persistence: "memory", BootID: "boot-1", StartedAt: now, ObservedAt: now, Location: chatarchivecmd.Location{Channel: "slackagent", AddressKey: "C1:1", Locator: "slackagent:C1:1"}}
	intent := chatarchivecmd.Intent{Token: "activation", Kind: "fresh", Session: rec, At: now}
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	// A live binding validation error rolls back archive activation too.
	invalid := SessionRecord{SessionID: "wrong"}
	if _, err := s.Activate(ctx, intent.Token, &invalid); !errors.Is(err, chatarchivecmd.ErrInvalid) {
		t.Fatalf("invalid binding=%v", err)
	}
	pending, err := s.Get(ctx, rec.ID)
	if err != nil || pending.Status != "pending" {
		t.Fatalf("rolled back activation=%+v,%v", pending, err)
	}
	if _, err := s.Activate(ctx, intent.Token, nil); err != nil {
		t.Fatal(err)
	}
	destruction := chatarchivecmd.Intent{Token: "reset", Kind: "destroy", Session: rec, OldID: rec.ID, Reason: "reset", At: now}
	if _, err := s.Begin(ctx, destruction); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, destruction.Token, "uncertain"); err != nil {
		t.Fatal(err)
	}
	unresolved, err := s.Pending(ctx, rec.Location.Channel, rec.Location.AddressKey)
	if err != nil || unresolved.Outcome != "uncertain" {
		t.Fatalf("pending uncertain=%+v,%v", unresolved, err)
	}
	if err := s.Complete(ctx, destruction.Token, "retained"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pending(ctx, rec.Location.Channel, rec.Location.AddressKey); !errors.Is(err, chatarchivecmd.ErrNotFound) {
		t.Fatalf("resolved operation=%v", err)
	}
	destruction.Token = "reset-again"
	if _, err := s.Begin(ctx, destruction); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, destruction.Token, "destroyed"); err != nil {
		t.Fatal(err)
	}
	ended, err := s.Get(ctx, rec.ID)
	if err != nil || ended.Status != "ended" || ended.EndReason != "reset" || ended.EndedAt.IsZero() {
		t.Fatalf("destroyed archive=%+v,%v", ended, err)
	}
	late := chatarchivecmd.Message{SessionID: rec.ID, Source: "agent:" + rec.ID + ":turn-1", Role: "agent", Text: "late old output", ObservedAt: now}
	if _, err := s.Record(ctx, late); err != nil {
		t.Fatal(err)
	}
	second := rec
	second.ID = "84fa05ee-f13a-4b05-ab54-9c6393654021"
	second.PredecessorID = rec.ID
	second.StartedAt = now.Add(time.Second)
	second.ObservedAt = second.StartedAt
	intent = chatarchivecmd.Intent{Token: "replacement", Kind: "fresh", Session: second, OldID: rec.ID, At: second.StartedAt}
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(ctx, intent.Token, nil); err != nil {
		t.Fatal(err)
	}
	page, err := s.List(ctx, chatarchivecmd.Filter{Limit: 1})
	if err != nil || len(page) != 1 || page[0].ID != second.ID {
		t.Fatalf("recent sessions=%+v,%v", page, err)
	}
	older, err := s.List(ctx, chatarchivecmd.Filter{Limit: 1, BeforeAt: page[0].ObservedAt, BeforeID: page[0].ID})
	if err != nil || len(older) != 1 || older[0].ID != rec.ID {
		t.Fatalf("older sessions=%+v,%v", older, err)
	}
	messages, err := s.Messages(ctx, second.ID, 0, 10)
	if err != nil || len(messages) != 0 {
		t.Fatalf("replacement transcript=%+v,%v", messages, err)
	}
	replay, err := s.FindMessage(ctx, late.Source)
	if err != nil || replay.SessionID != rec.ID {
		t.Fatalf("late attribution=%+v,%v", replay, err)
	}
	// Dedupe identities must never silently move to a replacement lifetime.
	late.SessionID = second.ID
	if _, err := s.Record(ctx, late); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("epoch reassignment=%v", err)
	}
}

func checkChatArchiveLifecycleFences(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	s := p.ChatArchive()
	ctx := t.Context()
	now := time.Now().UTC()
	rec := chatarchivecmd.Session{ID: "279fbf42-8955-44b7-b95b-51cd4e98ac33", StableID: "session-1", RuntimeID: "runtime-1", Persistence: "memory", BootID: "boot-1", StartedAt: now, ObservedAt: now, Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}}
	intent := chatarchivecmd.Intent{Token: "failed-creation", Kind: "fresh", Session: rec, At: now}
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, intent.Token, "aborted"); err != nil {
		t.Fatalf("abandon failed creation: %v", err)
	}
	replacement := rec
	replacement.ID = "84fa05ee-f13a-4b05-ab54-9c6393654021"
	intent.Token = "replacement"
	intent.Session = replacement
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatalf("reserve after failed creation: %v", err)
	}
	if _, err := s.Activate(ctx, intent.Token, nil); err != nil {
		t.Fatal(err)
	}
	wrong := SessionRecord{SessionID: "wrong", AddressJSON: "{}"}
	if _, err := s.Activate(ctx, intent.Token, &wrong); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("changed activation retry=%v", err)
	}
	exact := chatarchivecmd.Intent{Token: "wrong-runtime", Kind: "exact", Session: replacement, At: now}
	exact.Session.RuntimeID = "different-runtime"
	if _, err := s.Begin(ctx, exact); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("wrong runtime exact restore=%v", err)
	}
	destroy := chatarchivecmd.Intent{Token: "cross-binding", Kind: "destroy", Session: replacement, OldID: replacement.ID, At: now}
	destroy.Session.Location.AddressKey = "2:0"
	if _, err := s.Begin(ctx, destroy); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("cross-binding destroy=%v", err)
	}
	destroy.Token = "destroy"
	destroy.Session = replacement
	if _, err := s.Begin(ctx, destroy); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, destroy.Token, "destroyed"); err != nil {
		t.Fatal(err)
	}
	exact.Token = "ended-restore"
	exact.Session = replacement
	if _, err := s.Begin(ctx, exact); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("ended exact restore=%v", err)
	}
}

func checkChatArchiveExactRecovery(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	s := p.ChatArchive()
	ctx := t.Context()
	now := time.Now().UTC()
	rec := chatarchivecmd.Session{ID: "279fbf42-8955-44b7-b95b-51cd4e98ac33", StableID: "session-1", RuntimeID: "runtime-1", Persistence: "sqlite", BootID: "boot-1", StartedAt: now, ObservedAt: now, Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}}
	intent := chatarchivecmd.Intent{Token: "fresh", Kind: "fresh", Session: rec, At: now}
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(ctx, intent.Token, nil); err != nil {
		t.Fatal(err)
	}
	intent.Token = "exact"
	intent.Kind = "exact"
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, intent.Token, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, intent.Token, "aborted"); !errors.Is(err, chatarchivecmd.ErrConflict) {
		t.Fatalf("abandon uncertain exact restore=%v", err)
	}
	recovered, err := s.Activate(ctx, intent.Token, nil)
	if err != nil || recovered.ID != rec.ID || recovered.Status != "active" {
		t.Fatalf("same epoch recovery=%+v,%v", recovered, err)
	}
}

func checkChatArchiveExactRuntimeLoss(t *testing.T, open contractOpener) {
	p := newContractProvider(t, open)
	defer closeContractProvider(t, p)
	s := p.ChatArchive()
	ctx := t.Context()
	now := time.Now().UTC()
	rec := chatarchivecmd.Session{ID: "279fbf42-8955-44b7-b95b-51cd4e98ac33", StableID: "session-1", RuntimeID: "runtime-1", Persistence: "sqlite", BootID: "boot-1", StartedAt: now, ObservedAt: now, Location: chatarchivecmd.Location{Channel: "telegram", AddressKey: "1:0", Locator: "telegram:1:0"}}
	intent := chatarchivecmd.Intent{Token: "fresh", Kind: "fresh", Session: rec, At: now}
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Activate(ctx, intent.Token, nil); err != nil {
		t.Fatal(err)
	}
	intent.Token = "exact"
	intent.Kind = "exact"
	intent.Reason = "runtime_lost"
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, intent.Token, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := s.Complete(ctx, intent.Token, "destroyed"); err != nil {
		t.Fatalf("resolve proven runtime loss: %v", err)
	}
	old, err := s.Get(ctx, rec.ID)
	if err != nil || old.Status != "ended" {
		t.Fatalf("lost epoch=%+v,%v", old, err)
	}
	intent.Token = "replacement"
	intent.Kind = "fresh"
	intent.Session.ID = "84fa05ee-f13a-4b05-ab54-9c6393654021"
	if _, err := s.Begin(ctx, intent); err != nil {
		t.Fatalf("replacement after proven loss: %v", err)
	}
}
