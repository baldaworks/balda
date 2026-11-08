// Package loginlimit implements the bounded process-local password attempt limiter.
package loginlimit

import (
	"crypto/sha256"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	maxAttempts = 5
	window      = 5 * time.Minute
	maxEntries  = 4096
	staleAfter  = 15 * time.Minute
)

type entry struct {
	started  time.Time
	lastSeen time.Time
	attempts int
}

// Limiter bounds password attempts without retaining raw usernames or peers.
type Limiter struct {
	mu      sync.Mutex
	entries map[[sha256.Size]byte]entry
	now     func() time.Time
}

// New constructs an empty limiter. Restarting the process resets all entries.
func New() *Limiter {
	return &Limiter{entries: make(map[[sha256.Size]byte]entry), now: time.Now}
}

// Allow records one attempt and reports whether Core may be called.
func (l *Limiter) Allow(username, remoteAddr string) (bool, time.Duration) {
	now := l.now().UTC()
	key := digestKey(username, remoteAddr)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.expire(now)
	current, exists := l.entries[key]
	if !exists || now.Sub(current.started) >= window {
		l.ensureCapacity()
		l.entries[key] = entry{started: now, lastSeen: now, attempts: 1}
		return true, 0
	}
	current.lastSeen = now
	if current.attempts >= maxAttempts {
		l.entries[key] = current
		return false, current.started.Add(window).Sub(now)
	}
	current.attempts++
	l.entries[key] = current
	return true, 0
}

// Success forgets the attempt bucket after successful password authentication.
func (l *Limiter) Success(username, remoteAddr string) {
	l.mu.Lock()
	delete(l.entries, digestKey(username, remoteAddr))
	l.mu.Unlock()
}

func (l *Limiter) expire(now time.Time) {
	for key, current := range l.entries {
		if now.Sub(current.lastSeen) >= staleAfter {
			delete(l.entries, key)
		}
	}
}

func (l *Limiter) ensureCapacity() {
	if len(l.entries) < maxEntries {
		return
	}
	var oldestKey [sha256.Size]byte
	var oldest time.Time
	for key, current := range l.entries {
		if oldest.IsZero() || current.lastSeen.Before(oldest) || current.lastSeen.Equal(oldest) && keyLess(key, oldestKey) {
			oldestKey, oldest = key, current.lastSeen
		}
	}
	delete(l.entries, oldestKey)
}

func digestKey(username, remoteAddr string) [sha256.Size]byte {
	peer := strings.TrimSpace(remoteAddr)
	if host, _, err := net.SplitHostPort(peer); err == nil {
		peer = strings.Trim(host, "[]")
	}
	return sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(username)) + "\x00" + peer))
}

func keyLess(left, right [sha256.Size]byte) bool {
	for index := range left {
		if left[index] != right[index] {
			return left[index] < right[index]
		}
	}
	return false
}
