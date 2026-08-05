package state

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/dgraph-io/badger/v4"
	"github.com/normahq/balda/sessionmemory"
	"github.com/normahq/balda/sessionmemory/sessionmemorytest"
)

func TestBadgerSessionMemoryStoreCanonicalContract(t *testing.T) {
	sessionmemorytest.RunCanonicalStoreContract(t, func(t *testing.T) sessionmemory.CanonicalStore {
		store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
		if err != nil {
			t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
		}
		t.Cleanup(func() { _ = store.Close() })
		return store
	})
}

func TestBadgerSessionMemoryStoreOwnsOneWritableDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	first, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	if _, err := OpenBadgerSessionMemoryStore(directory); err == nil {
		t.Fatal("second writable Badger store opened")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	reopened, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = reopened.Close() })
}

func TestBadgerSessionMemoryRecordCodec(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	key := []byte("record")
	if err := store.db.Update(func(txn *badger.Txn) error {
		return putBadgerSessionMemoryRecord(txn, key, "scope", map[string]string{"key": "value"})
	}); err != nil {
		t.Fatalf("write record error = %v", err)
	}
	var got map[string]string
	if err := store.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, "scope", &got)
	}); err != nil {
		t.Fatalf("read record error = %v", err)
	}
	if got["key"] != "value" {
		t.Fatalf("record = %#v", got)
	}
	if err := store.db.View(func(txn *badger.Txn) error {
		return getBadgerSessionMemoryRecord(txn, key, "operation", &got)
	}); err == nil {
		t.Fatal("wrong record type decoded")
	}
}

func TestBadgerSessionMemoryStoreRequiresDirectory(t *testing.T) {
	if _, err := OpenBadgerSessionMemoryStore(" "); err == nil {
		t.Fatal("empty directory opened")
	}
}

func TestBadgerSessionMemoryStoreValueLogGCValidation(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := store.RunValueLogGC(0); err == nil {
		t.Fatal("RunValueLogGC(0) succeeded")
	}
	if err := store.RunValueLogGC(0.5); err != nil {
		t.Fatalf("RunValueLogGC() error = %v", err)
	}
}

func TestBadgerSessionMemoryStorePersistsEncryptedPayloadSeparately(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	ref := sessionmemory.PayloadRef{ID: "payload-1", KeyID: "kek-1", Digest: "digest-1", ByteSize: 1}
	encrypted := sessionmemory.EncryptedPayload{KeyID: ref.KeyID, PayloadHash: ref.Digest, Nonce: []byte{1}, Ciphertext: []byte{2}, DEKNonce: []byte{3}, WrappedDEK: []byte{4}}
	if err := store.PutEncryptedPayload(context.Background(), encrypted, ref); err != nil {
		t.Fatalf("PutEncryptedPayload() error = %v", err)
	}
	got, err := store.LoadEncryptedPayload(context.Background(), ref)
	if err != nil || string(got.Ciphertext) != string(encrypted.Ciphertext) {
		t.Fatalf("LoadEncryptedPayload() = %#v, error = %v", got, err)
	}
	if err := store.DeleteEncryptedPayload(context.Background(), ref); err != nil {
		t.Fatalf("DeleteEncryptedPayload() error = %v", err)
	}
	if _, err := store.LoadEncryptedPayload(context.Background(), ref); err == nil {
		t.Fatal("LoadEncryptedPayload() succeeded after scrub")
	}
}

func TestBadgerSessionMemoryStoreDeniesSourceByExactScope(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "canonical:deny-a", Kind: sessionmemory.ScopeKindPersonal}
	if err := store.DenySource(context.Background(), scope, "source-1", time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("DenySource() error = %v", err)
	}
	if err := store.DenySource(context.Background(), scope, "source-1", time.Date(2026, time.August, 5, 12, 1, 0, 0, time.UTC)); err != nil {
		t.Fatalf("DenySource() retry error = %v", err)
	}
	denied, err := store.IsSourceDenied(context.Background(), scope, "source-1")
	if err != nil || !denied {
		t.Fatalf("IsSourceDenied() = %t, error = %v", denied, err)
	}
	otherScope := sessionmemory.Scope{Key: "canonical:deny-b", Kind: sessionmemory.ScopeKindPersonal}
	if denied, err := store.IsSourceDenied(context.Background(), otherScope, "source-1"); err != nil || denied {
		t.Fatalf("other scope IsSourceDenied() = %t, error = %v", denied, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if denied, err := store.IsSourceDenied(context.Background(), scope, "source-1"); err != nil || !denied {
		t.Fatalf("reopened IsSourceDenied() = %t, error = %v", denied, err)
	}
}

func TestBadgerSessionMemoryStoreAppliesCanonicalMutation(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:badger", Kind: sessionmemory.ScopeKindPersonal}
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation: sessionmemory.OperationRecord{
			OperationID: "operation-1",
			Fingerprint: "derivation-v2-sha256",
			CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
		},
		Heads: []sessionmemory.ItemHead{{ItemID: "item-1", RevisionID: "revision-1"}},
	}
	mutation.Items = []sessionmemory.MemoryItem{canonicalItem(scope, "item-1")}
	mutation.Revisions = []sessionmemory.MemoryRevision{canonicalRevision("revision-1", "item-1")}
	first, err := store.ApplyCanonicalMutation(context.Background(), mutation)
	if err != nil {
		t.Fatalf("ApplyCanonicalMutation() error = %v", err)
	}
	if first.ScopeVersion != 1 || first.ChangeSeq != 1 {
		t.Fatalf("outcome = %#v", first)
	}
	replayed, err := store.ApplyCanonicalMutation(context.Background(), mutation)
	if err != nil || replayed.ScopeVersion != first.ScopeVersion || replayed.ChangeSeq != first.ChangeSeq {
		t.Fatalf("replay = %#v, error = %v, want %#v", replayed, err, first)
	}
	state, err := store.LoadScopeState(context.Background(), scope)
	if err != nil || state.Version != 1 || state.ChangeSeq != 1 {
		t.Fatalf("LoadScopeState() = %#v, error = %v", state, err)
	}
	changes, err := store.ScanScopeChanges(context.Background(), scope, 0, 10)
	if err != nil || len(changes) != 1 || changes[0].OperationID != mutation.Operation.OperationID {
		t.Fatalf("ScanScopeChanges() = %#v, error = %v", changes, err)
	}
	changes, err = store.ScanScopeChanges(context.Background(), scope, 1, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("ScanScopeChanges(after latest) = %#v, error = %v", changes, err)
	}
	collision := mutation
	collision.Operation.Fingerprint = "different-fingerprint"
	if _, err := store.ApplyCanonicalMutation(context.Background(), collision); err == nil {
		t.Fatal("operation fingerprint collision succeeded")
	}
}

func TestBadgerSessionMemoryStorePersistsBidirectionalProvenance(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:provenance", Kind: sessionmemory.ScopeKindPersonal}
	first := canonicalRevision("revision-1", "item-1")
	if _, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 0, "operation-1", first)); err != nil {
		t.Fatalf("first ApplyCanonicalMutation() error = %v", err)
	}
	second := canonicalRevision("revision-2", "item-2")
	second.Parents = []string{first.RevisionID}
	if _, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 1, "operation-2", second)); err != nil {
		t.Fatalf("second ApplyCanonicalMutation() error = %v", err)
	}
	forwardKey, err := badgerProvenanceKey(scope, badgerRecordProvenanceForward, second.RevisionID, first.RevisionID)
	if err != nil {
		t.Fatalf("badgerProvenanceKey() error = %v", err)
	}
	reverseKey, err := badgerProvenanceKey(scope, badgerRecordProvenanceReverse, first.RevisionID, second.RevisionID)
	if err != nil {
		t.Fatalf("badgerProvenanceKey() error = %v", err)
	}
	if err := store.db.View(func(txn *badger.Txn) error {
		var forward, reverse badgerProvenanceEdge
		if err := getBadgerSessionMemoryRecord(txn, forwardKey, badgerRecordProvenanceForward, &forward); err != nil {
			return err
		}
		if err := getBadgerSessionMemoryRecord(txn, reverseKey, badgerRecordProvenanceReverse, &reverse); err != nil {
			return err
		}
		if forward != reverse || forward.ChildRevisionID != second.RevisionID || forward.ParentRevisionID != first.RevisionID {
			t.Fatalf("provenance edges = %#v / %#v", forward, reverse)
		}
		return nil
	}); err != nil {
		t.Fatalf("read provenance edges: %v", err)
	}
	sourceKey, err := badgerProvenanceKey(scope, badgerRecordSourceRevision, "source-1", first.RevisionID)
	if err != nil {
		t.Fatalf("source provenance key error = %v", err)
	}
	if err := store.db.View(func(txn *badger.Txn) error {
		var edge badgerProvenanceEdge
		return getBadgerSessionMemoryRecord(txn, sourceKey, badgerRecordSourceRevision, &edge)
	}); err != nil {
		t.Fatalf("read source provenance edge: %v", err)
	}
}

func TestBadgerSessionMemoryStoreBatchesSourceProvenance(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:source-batch", Kind: sessionmemory.ScopeKindPersonal}
	for index, revisionID := range []string{"revision-1", "revision-2"} {
		mutation := canonicalRevisionMutation(scope, uint64(index), "operation-"+revisionID, canonicalRevision(revisionID, "item-"+revisionID))
		if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
			t.Fatalf("ApplyCanonicalMutation(%s) error = %v", revisionID, err)
		}
	}
	first, cursor, err := store.SourceRevisionBatch(context.Background(), scope, "source-1", "", 1)
	if err != nil || len(first) != 1 || cursor != first[0] {
		t.Fatalf("first SourceRevisionBatch() = %#v, %q, error = %v", first, cursor, err)
	}
	second, cursor, err := store.SourceRevisionBatch(context.Background(), scope, "source-1", cursor, 1)
	if err != nil || len(second) != 1 || second[0] == first[0] || cursor != second[0] {
		t.Fatalf("second SourceRevisionBatch() = %#v, %q, error = %v", second, cursor, err)
	}
	last, cursor, err := store.SourceRevisionBatch(context.Background(), scope, "source-1", cursor, 1)
	if err != nil || len(last) != 0 || cursor != "" {
		t.Fatalf("final SourceRevisionBatch() = %#v, %q, error = %v", last, cursor, err)
	}
}

func TestBadgerSessionMemoryStoreRejectsInvalidProvenanceAtomically(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:provenance-invalid", Kind: sessionmemory.ScopeKindPersonal}
	first := canonicalRevision("revision-1", "item-1")
	second := canonicalRevision("revision-2", "item-2")
	first.Parents = []string{second.RevisionID}
	second.Parents = []string{first.RevisionID}
	mutation := canonicalRevisionMutation(scope, 0, "operation-cycle", first)
	mutation.Revisions = append(mutation.Revisions, second)
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err == nil {
		t.Fatal("cyclic provenance mutation succeeded")
	}
	state, err := store.LoadScopeState(context.Background(), scope)
	if err != nil || state.Version != 0 || state.ChangeSeq != 0 {
		t.Fatalf("state after rejected mutation = %#v, error = %v", state, err)
	}
}

func TestBadgerSessionMemoryStoreRejectsDanglingHeadAtomically(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:dangling-head", Kind: sessionmemory.ScopeKindPersonal}
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation:     sessionmemory.OperationRecord{OperationID: "operation-1", Fingerprint: "fingerprint-1", CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)},
		Heads:         []sessionmemory.ItemHead{{ItemID: "item-1", RevisionID: "missing-revision"}},
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err == nil {
		t.Fatal("dangling head mutation succeeded")
	}
	state, err := store.LoadScopeState(context.Background(), scope)
	if err != nil || state.Version != 0 {
		t.Fatalf("state after rejected dangling head = %#v, error = %v", state, err)
	}
}

func TestBadgerSessionMemoryStoreClaimsAndRecoversDeliveryLease(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:delivery", Kind: sessionmemory.ScopeKindPersonal}
	mutation := sessionmemory.CanonicalMutation{
		SchemaVersion: sessionmemory.CanonicalSchemaVersionV1,
		Scope:         scope,
		Operation:     sessionmemory.OperationRecord{OperationID: "operation-1", Fingerprint: "fingerprint-1", CommittedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)},
		Delivery: []sessionmemory.DeliveryOutboxRecord{{
			DeliveryID: "delivery-1", OperationID: "operation-1", CreatedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC),
			PayloadRef: sessionmemory.PayloadRef{ID: "payload-1", KeyID: "key-1", Digest: "digest-1", ByteSize: 1},
		}},
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
		t.Fatalf("ApplyCanonicalMutation() error = %v", err)
	}
	now := time.Date(2026, time.August, 5, 12, 1, 0, 0, time.UTC)
	request := sessionmemory.DeliveryClaimRequest{Scope: scope, LeaseOwner: "worker-1", Now: now, LeaseUntil: now.Add(time.Minute), Limit: 1}
	first, err := store.ClaimDeliveryOutbox(context.Background(), request)
	if err != nil || len(first) != 1 || first[0].Claim.Attempts != 1 {
		t.Fatalf("first ClaimDeliveryOutbox() = %#v, error = %v", first, err)
	}
	if claimed, err := store.ClaimDeliveryOutbox(context.Background(), request); err != nil || len(claimed) != 0 {
		t.Fatalf("active lease claim = %#v, error = %v", claimed, err)
	}
	if err := store.SettleDeliveryOutbox(context.Background(), sessionmemory.DeliverySettlementRequest{
		Scope: scope, DeliveryID: "delivery-1", LeaseOwner: "worker-2", Status: sessionmemory.DeliveryStatusDelivered, CompletedAt: now,
	}); err == nil {
		t.Fatal("foreign worker settled an active lease")
	}
	if err := store.SettleDeliveryOutbox(context.Background(), sessionmemory.DeliverySettlementRequest{
		Scope: scope, DeliveryID: "delivery-1", LeaseOwner: "worker-1", Status: sessionmemory.DeliveryStatusDelivered, CompletedAt: now,
	}); err != nil {
		t.Fatalf("SettleDeliveryOutbox() error = %v", err)
	}
	if claimed, err := store.ClaimDeliveryOutbox(context.Background(), request); err != nil || len(claimed) != 0 {
		t.Fatalf("settled delivery claim = %#v, error = %v", claimed, err)
	}
	// Use a distinct delivery to cover lease recovery without disturbing the
	// delivered record above.
	mutation.Operation.OperationID = "operation-2"
	mutation.Operation.Fingerprint = "fingerprint-2"
	mutation.Operation.CommittedAt = now
	mutation.ExpectedScopeVersion = 1
	mutation.Delivery[0].DeliveryID = "delivery-2"
	mutation.Delivery[0].OperationID = "operation-2"
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err != nil {
		t.Fatalf("second ApplyCanonicalMutation() error = %v", err)
	}
	if claimed, err := store.ClaimDeliveryOutbox(context.Background(), request); err != nil || len(claimed) != 1 || claimed[0].Record.DeliveryID != "delivery-2" {
		t.Fatalf("second delivery claim = %#v, error = %v", claimed, err)
	}
	request.Now = request.LeaseUntil.Add(time.Second)
	request.LeaseUntil = request.Now.Add(time.Minute)
	recovered, err := store.ClaimDeliveryOutbox(context.Background(), request)
	if err != nil || len(recovered) != 1 || recovered[0].Claim.Attempts != 2 {
		t.Fatalf("recovered ClaimDeliveryOutbox() = %#v, error = %v", recovered, err)
	}
}

func TestBadgerSessionMemoryStoreRestoresReplayAndScopeState(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "canonical:restart", Kind: sessionmemory.ScopeKindPersonal}
	mutation := canonicalRevisionMutation(scope, 0, "operation-1", canonicalRevision("revision-1", "item-1"))
	first, err := store.ApplyCanonicalMutation(context.Background(), mutation)
	if err != nil {
		t.Fatalf("ApplyCanonicalMutation() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	replayed, err := store.ApplyCanonicalMutation(context.Background(), mutation)
	if err != nil || replayed.ScopeVersion != first.ScopeVersion || replayed.ChangeSeq != first.ChangeSeq {
		t.Fatalf("replay after reopen = %#v, error = %v", replayed, err)
	}
	state, err := store.LoadScopeState(context.Background(), scope)
	if err != nil || state.Version != first.ScopeVersion || state.ChangeSeq != first.ChangeSeq {
		t.Fatalf("state after reopen = %#v, error = %v", state, err)
	}
}

func TestBadgerSessionMemoryStoreFaultBeforeCommitLeavesNoPartialMutationAfterReopen(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "memory.badger")
	store, err := OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	scope := sessionmemory.Scope{Key: "canonical:fault", Kind: sessionmemory.ScopeKindPersonal}
	mutation := canonicalRevisionMutation(scope, 0, "operation-fault", canonicalRevision("revision-fault", "item-fault"))
	store.beforeCanonicalMutationCommit = func() error { return errors.New("injected pre-commit fault") }
	if _, err := store.ApplyCanonicalMutation(context.Background(), mutation); err == nil {
		t.Fatal("ApplyCanonicalMutation() error = nil, want injected pre-commit failure")
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	store, err = OpenBadgerSessionMemoryStore(directory)
	if err != nil {
		t.Fatalf("reopen error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	state, err := store.LoadScopeState(context.Background(), scope)
	if err != nil || state.Version != 0 || state.ChangeSeq != 0 {
		t.Fatalf("LoadScopeState(after failed commit) = %#v, error %v", state, err)
	}
	changes, err := store.ScanScopeChanges(context.Background(), scope, 0, 10)
	if err != nil || len(changes) != 0 {
		t.Fatalf("ScanScopeChanges(after failed commit) = %#v, error %v", changes, err)
	}
	outcome, err := store.ApplyCanonicalMutation(context.Background(), mutation)
	if err != nil || outcome.ScopeVersion != 1 || outcome.ChangeSeq != 1 {
		t.Fatalf("ApplyCanonicalMutation(retry) = %#v, error %v", outcome, err)
	}
}

func TestBadgerSessionMemoryStoreRejectsStaleCASAndAllowsIndependentScopes(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	scope := sessionmemory.Scope{Key: "canonical:cas", Kind: sessionmemory.ScopeKindPersonal}
	if _, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 0, "operation-1", canonicalRevision("revision-1", "item-1"))); err != nil {
		t.Fatalf("first ApplyCanonicalMutation() error = %v", err)
	}
	if _, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 0, "operation-2", canonicalRevision("revision-2", "item-2"))); err == nil {
		t.Fatal("stale CAS mutation succeeded")
	}
	var group sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"canonical:independent-a", "canonical:independent-b"} {
		group.Add(1)
		go func(key string) {
			defer group.Done()
			scope := sessionmemory.Scope{Key: key, Kind: sessionmemory.ScopeKindPersonal}
			_, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 0, "operation-1", canonicalRevision(key+"-revision", key+"-item")))
			errs <- err
		}(key)
	}
	group.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("independent scope mutation error = %v", err)
		}
	}
}

func TestBadgerSessionMemoryStoreIsolatesEqualIDsAcrossScopes(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(t.TempDir(), "memory.badger"))
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	for _, scope := range []sessionmemory.Scope{
		{Key: "canonical:scope-a", Kind: sessionmemory.ScopeKindPersonal},
		{Key: "canonical:scope-b", Kind: sessionmemory.ScopeKindPersonal},
	} {
		if _, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, 0, "operation-1", canonicalRevision("revision-1", "item-1"))); err != nil {
			t.Fatalf("ApplyCanonicalMutation(%q) error = %v", scope.Key, err)
		}
	}
}

func TestBadgerSessionMemoryErrorMapsTxnTooBig(t *testing.T) {
	err := badgerSessionMemoryError("test", badger.ErrTxnTooBig)
	var memoryErr *sessionmemory.Error
	if !errors.As(err, &memoryErr) || memoryErr.Code != sessionmemory.CodeLimitExceeded {
		t.Fatalf("badgerSessionMemoryError() = %#v", err)
	}
}

func canonicalRevisionMutation(scope sessionmemory.Scope, expectedVersion uint64, operationID string, revision sessionmemory.MemoryRevision) sessionmemory.CanonicalMutation {
	return sessionmemory.CanonicalMutation{
		SchemaVersion:        sessionmemory.CanonicalSchemaVersionV1,
		Scope:                scope,
		ExpectedScopeVersion: expectedVersion,
		Operation: sessionmemory.OperationRecord{
			OperationID: operationID,
			Fingerprint: operationID + "-fingerprint",
			CommittedAt: time.Date(2026, time.August, 5, 12, 0, int(expectedVersion), 0, time.UTC),
		},
		Items:     []sessionmemory.MemoryItem{canonicalItem(scope, revision.ItemID)},
		Revisions: []sessionmemory.MemoryRevision{revision},
	}
}

func canonicalItem(scope sessionmemory.Scope, itemID string) sessionmemory.MemoryItem {
	return sessionmemory.MemoryItem{ItemID: itemID, Scope: scope, Kind: sessionmemory.MemoryKindEvent}
}

func canonicalRevision(revisionID, itemID string) sessionmemory.MemoryRevision {
	return sessionmemory.MemoryRevision{
		SchemaVersion: sessionmemory.MemorySchemaVersionV2,
		RevisionID:    revisionID,
		ItemID:        itemID,
		Revision:      1,
		Temporal:      sessionmemory.Temporal{ObservedAt: time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)},
		Evidence: []sessionmemory.EvidenceRef{{
			SourceID: "source-1", MessageID: "message-1", Role: sessionmemory.MessageRoleUser,
			StartByte: 1, EndByte: 2, AssertionMode: sessionmemory.AssertionModeUser,
		}},
		Sensitivity: sessionmemory.SensitivityStandard,
		Retention:   sessionmemory.RetentionClassStandard,
		Payload:     sessionmemory.PayloadRef{ID: "payload-1", KeyID: "key-1", Digest: "digest-1", ByteSize: 1},
	}
}
