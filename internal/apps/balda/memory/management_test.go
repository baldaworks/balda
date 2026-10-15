package memory

import (
	"context"
	"errors"
	"math"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/memorycmd"
)

type managementPersistence struct {
	kv     *testKV
	fail   error
	denied error
}

func (p *managementPersistence) CheckAuthority(context.Context, memorycmd.Authority) error {
	return p.denied
}
func (p *managementPersistence) Commit(ctx context.Context, m memorycmd.Mutation) error {
	if p.fail != nil {
		return p.fail
	}
	return p.kv.SetJSON(ctx, memorycmd.Key, m.Record)
}

func TestManagementSharesOrderedMemoryAndRejectsStaleEdits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	kv := newTestKV()
	store := NewStore(kv, "", true)
	management := NewManagement(store, &managementPersistence{kv: kv})
	a := memorycmd.Authority{}
	first, err := management.Create(ctx, memorycmd.Create{Fact: "  alpha\n  multiline  ", Authority: a})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, "beta"); err != nil {
		t.Fatal(err)
	}
	inv, err := management.Inventory(ctx, "ALPHA", a)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Facts) != 1 || inv.Facts[0].Fact != "alpha\n  multiline" {
		t.Fatalf("inventory = %+v", inv)
	}
	editor, err := management.Get(ctx, first.ID, a)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Remember(ctx, "gamma"); err != nil {
		t.Fatal(err)
	}
	if _, err := management.Edit(ctx, memorycmd.Edit{ID: first.ID, Fact: "changed", ExpectedVersion: editor.ExpectedVersion}); !errors.Is(err, memorycmd.ErrConflict) {
		t.Fatalf("stale edit = %v", err)
	}
	inv, err = management.Inventory(ctx, "", a)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := management.Edit(ctx, memorycmd.Edit{ID: first.ID, Fact: "alpha2", ExpectedVersion: inv.Version})
	if err != nil {
		t.Fatal(err)
	}
	if changed.ID != first.ID || changed.CreatedAt != first.CreatedAt {
		t.Fatal("edit changed identity")
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "alpha2\n\nbeta\n\ngamma" {
		t.Fatalf("snapshot = %q", snapshot.Content)
	}
}

func TestManagementFinalDeleteKeepsInitializedBoundary(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, MemoryFileName), []byte("imported\nmultiline fact"), 0600); err != nil {
		t.Fatal(err)
	}
	kv := newTestKV()
	store := NewStore(kv, dir, true)
	management := NewManagement(store, &managementPersistence{kv: kv})
	inv, err := management.Inventory(ctx, "", memorycmd.Authority{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Facts) != 1 || inv.Facts[0].Fact != "imported\nmultiline fact" {
		t.Fatalf("import = %+v", inv)
	}
	if err := management.Delete(ctx, memorycmd.Delete{ID: inv.Facts[0].ID, ExpectedVersion: inv.Version}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := NewStore(kv, dir, true).Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "" || snapshot.Version != 2 || snapshot.UpdatedAt == "" {
		t.Fatalf("empty boundary = %+v", snapshot)
	}
	if _, found, err := kv.GetJSON(ctx, memorycmd.Key); err != nil || !found {
		t.Fatal("empty record missing")
	}
	if _, err := management.Create(ctx, memorycmd.Create{Fact: "next"}); err != nil {
		t.Fatal(err)
	}
	inv, err = management.Inventory(ctx, "", memorycmd.Authority{})
	if err != nil {
		t.Fatal(err)
	}
	if inv.Facts[0].ID != 3 {
		t.Fatal("deleted identity recycled")
	}
}

func TestManagementConcurrentRememberAndCreatesSurvive(t *testing.T) {
	t.Parallel()
	kv := newTestKV()
	store := NewStore(kv, "", true)
	management := NewManagement(store, &managementPersistence{kv: kv})
	var group sync.WaitGroup
	for range 20 {
		group.Go(func() {
			if _, err := store.Remember(t.Context(), "runtime"); err != nil {
				t.Error(err)
			}
		})
		group.Go(func() {
			if _, err := management.Create(t.Context(), memorycmd.Create{Fact: "UI"}); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	inv, err := management.Inventory(t.Context(), "", memorycmd.Authority{})
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.Facts) != 40 || inv.Version != 40 {
		t.Fatalf("concurrent inventory = %+v", inv)
	}
}

func TestManagementFailuresDoNotPublishChanges(t *testing.T) {
	t.Parallel()
	kv := newTestKV()
	store := NewStore(kv, "", true)
	persistence := &managementPersistence{kv: kv}
	management := NewManagement(store, persistence)
	fact, err := management.Create(t.Context(), memorycmd.Create{Fact: "original"})
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"", " \n\t"} {
		if _, err := management.Create(t.Context(), memorycmd.Create{Fact: text}); !errors.Is(err, memorycmd.ErrInvalid) {
			t.Fatalf("empty create = %v", err)
		}
		if _, err := management.Edit(t.Context(), memorycmd.Edit{ID: fact.ID, Fact: text, ExpectedVersion: 1}); !errors.Is(err, memorycmd.ErrInvalid) {
			t.Fatalf("empty edit = %v", err)
		}
	}
	if err := management.Delete(t.Context(), memorycmd.Delete{ID: fact.ID, ExpectedVersion: 2}); !errors.Is(err, memorycmd.ErrConflict) {
		t.Fatalf("stale delete = %v", err)
	}
	if err := management.Delete(t.Context(), memorycmd.Delete{ID: 99, ExpectedVersion: 1}); !errors.Is(err, memorycmd.ErrNotFound) {
		t.Fatalf("missing delete = %v", err)
	}
	if _, err := management.Get(t.Context(), 0, memorycmd.Authority{}); !errors.Is(err, memorycmd.ErrInvalid) {
		t.Fatalf("invalid id = %v", err)
	}
	persistence.fail = memorycmd.ErrUnavailable
	if _, err := management.Edit(t.Context(), memorycmd.Edit{ID: fact.ID, Fact: "uncommitted", ExpectedVersion: 1}); !errors.Is(err, memorycmd.ErrUnavailable) {
		t.Fatalf("failed commit = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Content != "original" || snapshot.Version != 1 {
		t.Fatalf("failed writes changed snapshot = %+v", snapshot)
	}
	persistence.denied = memorycmd.ErrForbidden
	if _, err := management.Inventory(t.Context(), "", memorycmd.Authority{}); !errors.Is(err, memorycmd.ErrForbidden) {
		t.Fatalf("authority = %v", err)
	}
}

type untouchedMemoryKV struct{}

func (untouchedMemoryKV) GetJSON(context.Context, string) (any, bool, error) {
	panic("disabled memory read")
}
func (untouchedMemoryKV) SetJSON(context.Context, string, any) error { panic("disabled memory write") }

func TestManagementDisabledDoesNotAccessMemory(t *testing.T) {
	t.Parallel()
	management := NewManagement(NewStore(untouchedMemoryKV{}, t.TempDir(), false), &managementPersistence{})
	inv, err := management.Inventory(t.Context(), "", memorycmd.Authority{})
	if err != nil || inv.Enabled || len(inv.Facts) != 0 {
		t.Fatalf("disabled inventory = %+v,%v", inv, err)
	}
	if _, err := management.Get(t.Context(), 1, memorycmd.Authority{}); !errors.Is(err, memorycmd.ErrDisabled) {
		t.Fatalf("disabled get = %v", err)
	}
	if _, err := management.Create(t.Context(), memorycmd.Create{Fact: "no write"}); !errors.Is(err, memorycmd.ErrDisabled) {
		t.Fatalf("disabled create = %v", err)
	}
	if _, err := management.Edit(t.Context(), memorycmd.Edit{ID: 1, Fact: "no write", ExpectedVersion: 1}); !errors.Is(err, memorycmd.ErrDisabled) {
		t.Fatalf("disabled edit = %v", err)
	}
	if err := management.Delete(t.Context(), memorycmd.Delete{ID: 1, ExpectedVersion: 1}); !errors.Is(err, memorycmd.ErrDisabled) {
		t.Fatalf("disabled delete = %v", err)
	}
}

func TestManagementRejectsCorruptRecordsAndOverflow(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	for _, rec := range []any{
		nil,
		memorycmd.Record{Version: 1, UpdatedAt: "private-canary"},
		memorycmd.Record{Version: 2, UpdatedAt: now, Entries: []memorycmd.Entry{{Version: 1, CreatedAt: now, Fact: "a"}, {Version: 1, CreatedAt: now, Fact: "b"}}},
		memorycmd.Record{Version: 1, UpdatedAt: now, Entries: []memorycmd.Entry{{Version: 0, CreatedAt: now, Fact: "a"}}},
		memorycmd.Record{Version: math.MaxInt64, UpdatedAt: now},
	} {
		kv := newTestKV()
		if err := kv.SetJSON(t.Context(), memorycmd.Key, rec); err != nil {
			t.Fatal(err)
		}
		store := NewStore(kv, "", true)
		management := NewManagement(store, &managementPersistence{kv: kv})
		if _, err := management.Create(t.Context(), memorycmd.Create{Fact: "not saved"}); !errors.Is(err, memorycmd.ErrUnavailable) {
			t.Fatalf("bad record create = %v", err)
		}
		if _, err := store.Remember(t.Context(), "not saved"); !errors.Is(err, memorycmd.ErrUnavailable) {
			t.Fatalf("bad record append = %v", err)
		}
	}
}
