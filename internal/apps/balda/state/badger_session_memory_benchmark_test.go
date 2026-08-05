package state

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/normahq/balda/sessionmemory"
)

// BenchmarkBadgerSessionMemoryStoreApplyCanonicalMutation100k records the
// fixed 100k-turn, one-scope longevity shape from the v2 acceptance contract.
// Run it with -benchtime=1x so every sample contains exactly 100,000 commits.
func BenchmarkBadgerSessionMemoryStoreApplyCanonicalMutation100k(b *testing.B) {
	const commits = 100_000
	if b.N != 1 {
		b.Fatalf("run with -benchtime=1x so the benchmark performs exactly %d commits", commits)
	}
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(b.TempDir(), "memory.badger"))
	if err != nil {
		b.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	ctx := context.Background()
	scope := sessionmemory.Scope{Key: "benchmark:canonical:100k", Kind: sessionmemory.ScopeKindPersonal}
	b.ResetTimer()
	started := time.Now()
	for index := 0; index < commits; index++ {
		id := fmt.Sprintf("%06d", index)
		mutation := canonicalRevisionMutation(
			scope,
			uint64(index),
			"operation-"+id,
			canonicalRevision("revision-"+id, "item-"+id),
		)
		if _, err := store.ApplyCanonicalMutation(ctx, mutation); err != nil {
			b.Fatalf("ApplyCanonicalMutation(%d) error = %v", index, err)
		}
	}
	b.StopTimer()
	b.ReportMetric(float64(time.Since(started))/commits, "ns/commit")
}
