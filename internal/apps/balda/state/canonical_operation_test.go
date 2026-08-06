package state

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

func TestCanonicalOperationCommitIsDurableAndIdempotent(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "canonical.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	scope := sessionmemory.Scope{Key: "canonical:operation", Kind: sessionmemory.ScopeKindPersonal}
	request := sessionmemory.CanonicalOperationCommitRequest{
		Scope: scope, OperationID: "operation-only", Fingerprint: "operation-only-fingerprint",
		CommittedAt: time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC),
	}
	first, err := store.CommitCanonicalOperation(context.Background(), request)
	if err != nil {
		t.Fatalf("CommitCanonicalOperation() error = %v", err)
	}
	second, err := store.CommitCanonicalOperation(context.Background(), request)
	if err != nil {
		t.Fatalf("CommitCanonicalOperation(replay) error = %v", err)
	}
	if !reflect.DeepEqual(first, second) || first.ScopeVersion != 1 || first.ChangeSeq != 1 {
		t.Fatalf("operation outcomes = %#v and %#v, want one durable outcome", first, second)
	}
	stored, found, err := store.LoadCanonicalOperation(context.Background(), scope, request.OperationID)
	if err != nil || !found {
		t.Fatalf("LoadCanonicalOperation() = %#v, found=%v, error=%v", stored, found, err)
	}
	if stored.Fingerprint != "operation-only-fingerprint" || !reflect.DeepEqual(stored.Outcome, first) {
		t.Fatalf("stored operation = %#v, want fingerprint/outcome %#v/%#v", stored, request.Fingerprint, first)
	}
	request.Fingerprint = "operation-only-conflict"
	if _, err := store.CommitCanonicalOperation(context.Background(), request); !hasStateCode(err, sessionmemory.CodeConflict) {
		t.Fatalf("CommitCanonicalOperation(identity reuse) error = %v, want conflict", err)
	}
}

func hasStateCode(err error, want sessionmemory.ErrorCode) bool {
	code, _, ok := sessionmemory.ClassifyError(err)
	return ok && code == want
}
