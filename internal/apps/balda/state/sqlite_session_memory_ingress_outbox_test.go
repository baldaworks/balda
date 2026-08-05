package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/normahq/balda/internal/apps/balda/sessionmemorycmd"
	"github.com/normahq/balda/sessionmemory"
)

func TestSQLiteSessionMemoryIngressOutboxPersistsFIFOClaimsAcrossRestart(t *testing.T) {
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "state.db")
	provider, err := NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	first := testIngressRecord(t, "turn-1", now)
	second := testIngressRecord(t, "turn-2", now.Add(time.Second))
	storedFirst, created, err := store.EnqueueSessionMemoryIngress(ctx, first)
	if err != nil || !created || storedFirst.ScopeSequence != 1 {
		t.Fatalf("first EnqueueSessionMemoryIngress() = %#v, created %t, error %v", storedFirst, created, err)
	}
	storedSecond, created, err := store.EnqueueSessionMemoryIngress(ctx, second)
	if err != nil || !created || storedSecond.ScopeSequence != 2 {
		t.Fatalf("second EnqueueSessionMemoryIngress() = %#v, created %t, error %v", storedSecond, created, err)
	}
	replay, created, err := store.EnqueueSessionMemoryIngress(ctx, first)
	if err != nil || created || replay.ScopeSequence != 1 {
		t.Fatalf("replay EnqueueSessionMemoryIngress() = %#v, created %t, error %v", replay, created, err)
	}
	leaseUntil := now.Add(time.Minute)
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, leaseUntil, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ExportID() != first.ExportID() || claimed[0].Attempts != 1 {
		t.Fatalf("first ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	if err := store.MarkSessionMemoryIngressPublished(ctx, first.ExportID(), "worker-1", now); err != nil {
		t.Fatalf("MarkSessionMemoryIngressPublished() error = %v", err)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	provider, err = NewSQLiteProvider(ctx, dbPath)
	if err != nil {
		t.Fatalf("reopen NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	claimed, err = provider.SessionMemoryIngressOutbox().ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(time.Second), now.Add(2*time.Minute), 10)
	if err != nil || len(claimed) != 1 || claimed[0].ExportID() != second.ExportID() || claimed[0].ScopeSequence != 2 {
		t.Fatalf("reopened ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
}

func TestSQLiteSessionMemoryIngressOutboxRecoversExpiredLeaseAndRejectsForeignSettlement(t *testing.T) {
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	record := testIngressRecord(t, "turn-1", now)
	stored, _, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil {
		t.Fatalf("EnqueueSessionMemoryIngress() error = %v", err)
	}
	if _, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, now.Add(time.Minute), 1); err != nil {
		t.Fatalf("ClaimSessionMemoryIngress() error = %v", err)
	}
	if err := store.MarkSessionMemoryIngressPublished(ctx, stored.ExportID(), "worker-2", now); err == nil {
		t.Fatal("foreign worker settled ingress lease")
	}
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(time.Minute+time.Second), now.Add(2*time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 || claimed[0].LeaseOwner != "worker-2" {
		t.Fatalf("recovered ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	if err := store.ReleaseSessionMemoryIngress(ctx, stored.ExportID(), "worker-2", "temporary", false, now.Add(time.Minute+2*time.Second)); err != nil {
		t.Fatalf("ReleaseSessionMemoryIngress() error = %v", err)
	}
}

func testIngressRecord(t *testing.T, sourceTurnID string, completedAt time.Time) sessionmemorycmd.IngressRecord {
	t.Helper()
	turn, err := sessionmemory.NewTurn(
		sessionmemory.Scope{Key: "telegram:ingress:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-1", AgentSessionID: "session-1"},
		sourceTurnID, completedAt, "hello", "hi",
	)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn export error = %v", err)
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, completedAt)
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	return record
}
