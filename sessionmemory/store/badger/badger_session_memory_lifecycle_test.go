package badger

import (
	"context"
	"testing"
	"time"
)

func TestCanonicalMaintenanceOwnsStartStopLifecycle(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(t.TempDir() + "/memory.badger")
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	maintenance, err := NewCanonicalMaintenance(store, CanonicalMaintenanceConfig{ValueLogGCInterval: time.Millisecond})
	if err != nil {
		_ = store.Close()
		t.Fatalf("NewCanonicalMaintenance() error = %v", err)
	}
	ctx := context.Background()
	if err := maintenance.Start(ctx); err != nil {
		_ = store.Close()
		t.Fatalf("CanonicalMaintenance.Start() error = %v", err)
	}
	if err := maintenance.Start(ctx); err == nil {
		t.Fatal("CanonicalMaintenance.Start() accepted a second worker")
	}
	stopCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := maintenance.Stop(stopCtx); err != nil {
		_ = store.Close()
		t.Fatalf("CanonicalMaintenance.Stop() error = %v", err)
	}
	if err := maintenance.Stop(ctx); err != nil {
		t.Fatalf("CanonicalMaintenance.Stop(replay) error = %v", err)
	}
	if err := maintenance.Close(ctx); err != nil {
		t.Fatalf("CanonicalMaintenance.Close() error = %v", err)
	}
}

func TestNewCanonicalMaintenanceValidatesOperationalBounds(t *testing.T) {
	store, err := OpenBadgerSessionMemoryStore(t.TempDir() + "/memory.badger")
	if err != nil {
		t.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if _, err := NewCanonicalMaintenance(store, CanonicalMaintenanceConfig{ValueLogGCInterval: -time.Second}); err == nil {
		t.Fatal("NewCanonicalMaintenance() accepted a negative interval")
	}
	if _, err := NewCanonicalMaintenance(store, CanonicalMaintenanceConfig{DiscardRatio: 1}); err == nil {
		t.Fatal("NewCanonicalMaintenance() accepted an invalid discard ratio")
	}
}
