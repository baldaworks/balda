package actors

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

type TaskRunRegistry struct {
	mu      sync.Mutex
	nextID  uint64
	cancels map[string]map[string]context.CancelFunc
}

func NewTaskRunRegistry() *TaskRunRegistry {
	return &TaskRunRegistry{cancels: make(map[string]map[string]context.CancelFunc)}
}

func (r *TaskRunRegistry) Register(taskID string, cancel context.CancelFunc) string {
	if r == nil || cancel == nil {
		return ""
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.nextID++
	runID := strconv.FormatUint(r.nextID, 10)
	runs := r.cancels[trimmed]
	if runs == nil {
		runs = make(map[string]context.CancelFunc)
		r.cancels[trimmed] = runs
	}
	runs[runID] = cancel
	return runID
}

func (r *TaskRunRegistry) Unregister(taskID string, runID string) {
	if r == nil {
		return
	}
	trimmedTaskID := strings.TrimSpace(taskID)
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedTaskID == "" || trimmedRunID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := r.cancels[trimmedTaskID]
	if runs == nil {
		return
	}
	delete(runs, trimmedRunID)
	if len(runs) == 0 {
		delete(r.cancels, trimmedTaskID)
	}
}

func (r *TaskRunRegistry) Cancel(taskID string) bool {
	if r == nil {
		return false
	}
	trimmed := strings.TrimSpace(taskID)
	if trimmed == "" {
		return false
	}
	r.mu.Lock()
	runs := r.cancels[trimmed]
	delete(r.cancels, trimmed)
	cancelFuncs := make([]context.CancelFunc, 0, len(runs))
	for _, cancel := range runs {
		cancelFuncs = append(cancelFuncs, cancel)
	}
	r.mu.Unlock()
	if len(cancelFuncs) == 0 {
		return false
	}
	canceled := false
	for _, cancel := range cancelFuncs {
		if cancel == nil {
			continue
		}
		cancel()
		canceled = true
	}
	return canceled
}
