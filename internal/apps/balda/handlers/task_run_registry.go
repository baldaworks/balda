package handlers

import (
	"context"
	"strings"
	"sync"
)

type taskRunRegistry struct {
	mu      sync.Mutex
	cancels map[string]context.CancelFunc
}

func newTaskRunRegistry() *taskRunRegistry {
	return &taskRunRegistry{cancels: make(map[string]context.CancelFunc)}
}

func (r *taskRunRegistry) register(taskID string, cancel context.CancelFunc) {
	if r == nil || cancel == nil {
		return
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return
	}
	r.mu.Lock()
	r.cancels[trimmed] = cancel
	r.mu.Unlock()
}

func (r *taskRunRegistry) unregister(taskID string) {
	if r == nil {
		return
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return
	}
	r.mu.Lock()
	delete(r.cancels, trimmed)
	r.mu.Unlock()
}

func (r *taskRunRegistry) cancel(taskID string) bool {
	if r == nil {
		return false
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return false
	}
	r.mu.Lock()
	cancel := r.cancels[trimmed]
	delete(r.cancels, trimmed)
	r.mu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}
