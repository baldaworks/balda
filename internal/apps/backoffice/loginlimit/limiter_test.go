package loginlimit

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiterBoundsAttemptsAndResets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := New()
	limiter.now = func() time.Time { return now }
	for range maxAttempts {
		allowed, retry := limiter.Allow(" User ", "127.0.0.1:1234")
		require.True(t, allowed)
		require.Zero(t, retry)
	}
	allowed, retry := limiter.Allow("user", "127.0.0.1:9999")
	require.False(t, allowed)
	require.Equal(t, window, retry)

	limiter.Success("USER", "127.0.0.1:5")
	allowed, _ = limiter.Allow("user", "127.0.0.1:6")
	require.True(t, allowed)

	now = now.Add(window)
	allowed, _ = limiter.Allow("user", "127.0.0.1:7")
	require.True(t, allowed)
}

func TestLimiterCapacityAndConcurrency(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	limiter := New()
	limiter.now = func() time.Time { return now }
	for index := 0; index < maxEntries+20; index++ {
		allowed, _ := limiter.Allow(fmt.Sprintf("user-%d", index), "127.0.0.1:1")
		require.True(t, allowed)
	}
	require.Len(t, limiter.entries, maxEntries)

	limiter = New()
	limiter.now = func() time.Time { return now }
	var wait sync.WaitGroup
	results := make(chan bool, 40)
	for range 40 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			allowed, _ := limiter.Allow("shared", "127.0.0.1:1")
			results <- allowed
		}()
	}
	wait.Wait()
	close(results)
	allowedCount := 0
	for allowed := range results {
		if allowed {
			allowedCount++
		}
	}
	require.Equal(t, maxAttempts, allowedCount)
}
