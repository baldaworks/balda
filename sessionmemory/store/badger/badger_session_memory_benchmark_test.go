package badger

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/sessionmemory"
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

// BenchmarkBadgerSessionMemoryStoreApplyCanonicalMutation128Scopes measures
// the acceptance concurrency shape: 50,000 mutations across 128 independent,
// ordered scope lanes. Run with -benchtime=1x and compare p95/p99 with the
// documented hardware-specific thresholds rather than treating them as CI
// pass/fail values.
func BenchmarkBadgerSessionMemoryStoreApplyCanonicalMutation128Scopes(b *testing.B) {
	const (
		scopes       = 128
		commits      = 50_000
		basePerScope = commits / scopes
		extraScopes  = commits % scopes
	)
	if b.N != 1 {
		b.Fatalf("run with -benchtime=1x so the benchmark performs exactly %d commits", commits)
	}
	store, err := OpenBadgerSessionMemoryStore(filepath.Join(b.TempDir(), "memory.badger"))
	if err != nil {
		b.Fatalf("OpenBadgerSessionMemoryStore() error = %v", err)
	}
	b.Cleanup(func() { _ = store.Close() })

	durations := make(chan time.Duration, commits)
	errs := make(chan error, scopes)
	var group sync.WaitGroup
	b.ResetTimer()
	for scopeIndex := 0; scopeIndex < scopes; scopeIndex++ {
		count := basePerScope
		if scopeIndex < extraScopes {
			count++
		}
		group.Add(1)
		go func(scopeIndex, count int) {
			defer group.Done()
			scope := sessionmemory.Scope{Key: fmt.Sprintf("benchmark:canonical:scope:%03d", scopeIndex), Kind: sessionmemory.ScopeKindPersonal}
			for index := 0; index < count; index++ {
				id := fmt.Sprintf("%03d-%06d", scopeIndex, index)
				started := time.Now()
				_, err := store.ApplyCanonicalMutation(context.Background(), canonicalRevisionMutation(scope, uint64(index), "operation-"+id, canonicalRevision("revision-"+id, "item-"+id)))
				durations <- time.Since(started)
				if err != nil {
					errs <- fmt.Errorf("scope %d mutation %d: %w", scopeIndex, index, err)
					return
				}
			}
		}(scopeIndex, count)
	}
	group.Wait()
	b.StopTimer()
	close(errs)
	for err := range errs {
		b.Fatal(err)
	}
	close(durations)
	values := make([]time.Duration, 0, commits)
	for duration := range durations {
		values = append(values, duration)
	}
	if len(values) != commits {
		b.Fatalf("completed mutations = %d, want %d", len(values), commits)
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	b.ReportMetric(float64(values[percentileIndex(len(values), 0.95)])/float64(time.Millisecond), "apply_p95_ms")
	b.ReportMetric(float64(values[percentileIndex(len(values), 0.99)])/float64(time.Millisecond), "apply_p99_ms")
}

func percentileIndex(length int, percentile float64) int {
	if length <= 1 {
		return 0
	}
	return int(float64(length-1) * percentile)
}
