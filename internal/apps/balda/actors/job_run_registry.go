package actors

import (
	"context"
	"strconv"
	"strings"
	"sync"
)

type JobRunRegistry struct {
	mu      sync.Mutex
	nextID  uint64
	cancels map[string]map[string]context.CancelFunc
}

func NewJobRunRegistry() *JobRunRegistry {
	return &JobRunRegistry{cancels: make(map[string]map[string]context.CancelFunc)}
}

func (r *JobRunRegistry) Register(jobID string, cancel context.CancelFunc) string {
	if r == nil || cancel == nil {
		return ""
	}
	trimmed := strings.TrimSpace(jobID)
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

func (r *JobRunRegistry) Unregister(jobID string, runID string) {
	if r == nil {
		return
	}
	trimmedJobID := strings.TrimSpace(jobID)
	trimmedRunID := strings.TrimSpace(runID)
	if trimmedJobID == "" || trimmedRunID == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	runs := r.cancels[trimmedJobID]
	if runs == nil {
		return
	}
	delete(runs, trimmedRunID)
	if len(runs) == 0 {
		delete(r.cancels, trimmedJobID)
	}
}

func (r *JobRunRegistry) Cancel(jobID string) bool {
	if r == nil {
		return false
	}
	trimmed := strings.TrimSpace(jobID)
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
