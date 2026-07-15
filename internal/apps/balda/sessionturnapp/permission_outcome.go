package sessionturnapp

import (
	"strings"
	"sync"

	"github.com/normahq/balda/internal/apps/balda/permissioncmd"
)

type permissionOutcomeRecorder struct {
	mu       sync.Mutex
	latest   permissioncmd.Outcome
	recorded bool
}

func (r *permissionOutcomeRecorder) RecordPermissionOutcome(outcome permissioncmd.Outcome) {
	if r == nil || outcome.Kind == permissioncmd.OutcomeAllowed {
		return
	}
	r.mu.Lock()
	r.latest = outcome
	r.recorded = true
	r.mu.Unlock()
}

func (r *permissionOutcomeRecorder) Latest() (permissioncmd.Outcome, bool) {
	if r == nil {
		return permissioncmd.Outcome{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.latest, r.recorded
}

func permissionOutcomeTurnMessage(outcome permissioncmd.Outcome, ok bool) string {
	if !ok || outcome.Kind == permissioncmd.OutcomeAllowed {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(outcome.Source)) {
	case "timeout":
		return "Permission request timed out. The requested action was denied."
	case "delivery_failed":
		return "The permission request could not be delivered. The requested action was denied."
	default:
		return "Permission denied. The requested action was not performed."
	}
}
