package hooks

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/hookcmd"
)

type deduplicationKey struct {
	sessionID    string
	invocationID string
	toolCallID   string
	event        hookcmd.EventName
}

type invocationKey struct {
	sessionID    string
	invocationID string
}

func deduplicationKeyFor(event hookcmd.Event) (deduplicationKey, bool) {
	key := deduplicationKey{
		sessionID:    event.SessionID,
		invocationID: event.InvocationID,
		toolCallID:   event.ToolCallID,
		event:        event.Name,
	}
	return key, event.Name.ToolEvent() && key.sessionID != "" && key.invocationID != "" && key.toolCallID != ""
}

type coalescingCache struct {
	mu          sync.Mutex
	entries     map[deduplicationKey]*coalescingEntry
	invocations map[invocationKey]int
	now         func() time.Time
	ttl         time.Duration
}

type coalescingEntry struct {
	done      chan struct{}
	result    hookcmd.Result
	err       classifiedError
	expiresAt time.Time
	released  bool
}

type classifiedError struct {
	kind   error
	hook   string
	reason string
}

func newCoalescingCache() coalescingCache {
	return coalescingCache{
		entries:     make(map[deduplicationKey]*coalescingEntry),
		invocations: make(map[invocationKey]int),
		now:         time.Now,
		ttl:         deduplicationTTL,
	}
}

func (c *coalescingCache) run(ctx context.Context, key deduplicationKey, execute func() (hookcmd.Result, error)) (hookcmd.Result, error) {
	c.mu.Lock()
	c.pruneLocked(c.now())
	if entry := c.entries[key]; entry != nil {
		c.mu.Unlock()
		return waitForDecision(ctx, entry)
	}
	invocation := invocationKey{sessionID: key.sessionID, invocationID: key.invocationID}
	if len(c.entries) >= maxGlobalEntries || c.invocations[invocation] >= maxInvocationEntries {
		c.mu.Unlock()
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
	entry := &coalescingEntry{done: make(chan struct{})}
	c.entries[key] = entry
	c.invocations[invocation]++
	c.mu.Unlock()

	result, err := execute()
	c.mu.Lock()
	entry.result = cloneResult(result)
	entry.err = classifyError(err)
	entry.expiresAt = c.now().Add(c.ttl)
	close(entry.done)
	if entry.released {
		c.deleteLocked(key)
	}
	c.mu.Unlock()
	return cloneResult(result), entry.err.value()
}

func waitForDecision(ctx context.Context, entry *coalescingEntry) (hookcmd.Result, error) {
	select {
	case <-entry.done:
		return cloneResult(entry.result), entry.err.value()
	case <-ctx.Done():
		return hookcmd.Result{}, hookcmd.ErrExecution
	}
}

func (c *coalescingCache) releaseInvocation(sessionID, invocationID string) {
	c.release(func(key deduplicationKey) bool {
		return key.sessionID == sessionID && key.invocationID == invocationID
	})
}

func (c *coalescingCache) releaseSession(sessionID string) {
	c.release(func(key deduplicationKey) bool { return key.sessionID == sessionID })
}

func (c *coalescingCache) release(matches func(deduplicationKey) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, entry := range c.entries {
		if !matches(key) {
			continue
		}
		select {
		case <-entry.done:
			c.deleteLocked(key)
		default:
			entry.released = true
		}
	}
}

func (c *coalescingCache) pruneLocked(now time.Time) {
	for key, entry := range c.entries {
		if !entry.expiresAt.IsZero() && !now.Before(entry.expiresAt) {
			c.deleteLocked(key)
		}
	}
}

func (c *coalescingCache) deleteLocked(key deduplicationKey) {
	if _, ok := c.entries[key]; !ok {
		return
	}
	delete(c.entries, key)
	invocation := invocationKey{sessionID: key.sessionID, invocationID: key.invocationID}
	if c.invocations[invocation] <= 1 {
		delete(c.invocations, invocation)
	} else {
		c.invocations[invocation]--
	}
}

func classifyError(err error) classifiedError {
	if err == nil {
		return classifiedError{}
	}
	var denied *hookcmd.DeniedError
	if errors.As(err, &denied) {
		return classifiedError{kind: hookcmd.ErrDenied, hook: denied.Hook, reason: denied.Reason}
	}
	for _, kind := range []error{hookcmd.ErrInvalid, hookcmd.ErrForbidden, hookcmd.ErrConflict, hookcmd.ErrNotFound, hookcmd.ErrUnavailable, hookcmd.ErrExecution, hookcmd.ErrUnsupported, hookcmd.ErrSessionReset} {
		if errors.Is(err, kind) {
			return classifiedError{kind: kind}
		}
	}
	return classifiedError{kind: hookcmd.ErrUnavailable}
}

func (e classifiedError) value() error {
	if e.kind == nil {
		return nil
	}
	if e.kind == hookcmd.ErrDenied {
		return &hookcmd.DeniedError{Hook: e.hook, Reason: e.reason}
	}
	return e.kind
}

func cloneResult(result hookcmd.Result) hookcmd.Result {
	result.UpdatedInput = append(result.UpdatedInput[:0:0], result.UpdatedInput...)
	result.UpdatedOutput = append(result.UpdatedOutput[:0:0], result.UpdatedOutput...)
	return result
}
