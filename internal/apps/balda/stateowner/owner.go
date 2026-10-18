// Package stateowner provides process-level exclusive ownership of a Balda
// state directory.
package stateowner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gofrs/flock"
)

var (
	// ErrActive means another Balda or maintenance process owns the state directory.
	ErrActive = errors.New("balda state directory is already in use")
	// ErrUnavailable means ownership could not be established safely.
	ErrUnavailable = errors.New("balda state directory ownership is unavailable")
)

// Owner holds the state-directory lock until Close.
type Owner struct {
	mu     sync.Mutex
	lock   *flock.Flock
	closed bool
}

// Acquire attempts exclusive ownership without waiting.
func Acquire(stateDir string) (*Owner, error) {
	dir := strings.TrimSpace(stateDir)
	if dir == "" {
		return nil, ErrUnavailable
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ErrUnavailable
	}
	lock := flock.New(filepath.Join(dir, "balda.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		_ = lock.Close()
		return nil, ErrUnavailable
	}
	if !locked {
		_ = lock.Close()
		return nil, ErrActive
	}
	return &Owner{lock: lock}, nil
}

// Close releases ownership. It is safe to call more than once.
func (o *Owner) Close() error {
	if o == nil {
		return nil
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.closed {
		return nil
	}
	o.closed = true
	if o.lock == nil {
		return nil
	}
	if err := o.lock.Unlock(); err != nil {
		_ = o.lock.Close()
		return ErrUnavailable
	}
	if err := o.lock.Close(); err != nil {
		return ErrUnavailable
	}
	return nil
}
