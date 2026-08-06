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

func TestSQLiteSessionMemoryIngressOutboxReplaysTypedToolEvidenceWithoutDuplication(t *testing.T) {
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	completedAt := time.Date(2026, time.August, 6, 12, 0, 0, 0, time.UTC)
	turn, err := sessionmemory.NewTerminalTurnWithTools(
		sessionmemory.Scope{Key: "telegram:ingress-tools:0", Kind: sessionmemory.ScopeKindPersonal},
		sessionmemory.SessionRef{SessionID: "session-tools", AgentSessionID: "agent-tools"},
		"turn-tools", completedAt, "question", "answer",
		[]sessionmemory.Message{{ToolName: "calendar.lookup", ToolCallID: "call-1", Text: "2026-08-06"}},
		sessionmemory.TurnTerminalStatusSuccess,
	)
	if err != nil {
		t.Fatalf("NewTerminalTurnWithTools() error = %v", err)
	}
	export, err := sessionmemorycmd.NewTurn(turn)
	if err != nil {
		t.Fatalf("NewTurn() error = %v", err)
	}
	record, err := sessionmemorycmd.NewIngressRecord(export, completedAt)
	if err != nil {
		t.Fatalf("NewIngressRecord() error = %v", err)
	}
	store := provider.SessionMemoryIngressOutbox()
	stored, created, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil || !created {
		t.Fatalf("first EnqueueSessionMemoryIngress() = %#v, created %t, error %v", stored, created, err)
	}
	replay, created, err := store.EnqueueSessionMemoryIngress(ctx, record)
	if err != nil || created || replay.ScopeSequence != stored.ScopeSequence || replay.Export.Turn == nil || len(replay.Export.Turn.Messages) != 3 {
		t.Fatalf("replay EnqueueSessionMemoryIngress() = %#v, created %t, error %v", replay, created, err)
	}
	tool := replay.Export.Turn.Messages[2]
	if tool.Role != sessionmemory.MessageRoleTool || tool.MessageID != sessionmemory.TurnToolMessageID(turn.ExportID, "calendar.lookup", "call-1") || tool.Text != "2026-08-06" {
		t.Fatalf("replayed tool evidence = %#v", tool)
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
	releasedAt := now.Add(time.Minute + 2*time.Second)
	retryAt := releasedAt.Add(10 * time.Second)
	if err := store.ReleaseSessionMemoryIngress(ctx, stored.ExportID(), "worker-2", "temporary", false, &retryAt, releasedAt); err != nil {
		t.Fatalf("ReleaseSessionMemoryIngress() error = %v", err)
	}
	claimed, err = store.ClaimSessionMemoryIngress(ctx, "worker-3", retryAt.Add(-time.Nanosecond), retryAt.Add(time.Minute), 1)
	if err != nil || len(claimed) != 0 {
		t.Fatalf("early ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	claimed, err = store.ClaimSessionMemoryIngress(ctx, "worker-3", retryAt, retryAt.Add(time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 3 {
		t.Fatalf("retry ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
}

func TestSQLiteSessionMemoryIngressOutboxReplaysTerminalWithAuditAndStats(t *testing.T) {
	ctx := context.Background()
	provider, err := NewSQLiteProvider(ctx, filepath.Join(t.TempDir(), "state.db"))
	if err != nil {
		t.Fatalf("NewSQLiteProvider() error = %v", err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	store := provider.SessionMemoryIngressOutbox()
	now := time.Date(2026, time.August, 5, 12, 0, 0, 0, time.UTC)
	stored, _, err := store.EnqueueSessionMemoryIngress(ctx, testIngressRecord(t, "turn-terminal", now))
	if err != nil {
		t.Fatalf("EnqueueSessionMemoryIngress() error = %v", err)
	}
	if _, err := store.ClaimSessionMemoryIngress(ctx, "worker-1", now, now.Add(time.Minute), 1); err != nil {
		t.Fatalf("ClaimSessionMemoryIngress() error = %v", err)
	}
	if err := store.ReleaseSessionMemoryIngress(ctx, stored.ExportID(), "worker-1", "limit reached", true, nil, now.Add(time.Second)); err != nil {
		t.Fatalf("ReleaseSessionMemoryIngress() error = %v", err)
	}
	stats, err := store.SessionMemoryIngressStats(ctx, now.Add(2*time.Minute))
	if err != nil || stats.PendingCount != 0 || stats.TerminalCount != 1 {
		t.Fatalf("SessionMemoryIngressStats() = %#v, error %v", stats, err)
	}
	if err := store.ReplaySessionMemoryIngress(ctx, stored.ExportID(), "operator-1", "fixed transport", now.Add(2*time.Minute)); err != nil {
		t.Fatalf("ReplaySessionMemoryIngress() error = %v", err)
	}
	stats, err = store.SessionMemoryIngressStats(ctx, now.Add(2*time.Minute))
	if err != nil || stats.PendingCount != 1 || stats.TerminalCount != 0 || stats.OldestPendingAge != 2*time.Minute {
		t.Fatalf("SessionMemoryIngressStats() after replay = %#v, error %v", stats, err)
	}
	claimed, err := store.ClaimSessionMemoryIngress(ctx, "worker-2", now.Add(2*time.Minute), now.Add(3*time.Minute), 1)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("replayed ClaimSessionMemoryIngress() = %#v, error %v", claimed, err)
	}
	concrete := provider.(*sqliteProvider)
	var auditCount int
	if err := concrete.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM session_memory_ingress_audit WHERE export_id = ? AND action = 'replay_terminal' AND actor = 'operator-1'`, stored.ExportID()).Scan(&auditCount); err != nil || auditCount != 1 {
		t.Fatalf("replay audit count = %d, error %v", auditCount, err)
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
