package mcpruntime

import (
	"context"
	"errors"
)

// Retry restarts only named failed exact attachments, preserving ready leases.
// The caller owns the authorization event and current selection policy.
func (r *Reconciler) Retry(ctx context.Context, keys []InstanceKey) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var failures []error
	for _, key := range keys {
		if err := ctx.Err(); err != nil {
			return err
		}
		current := r.instances[key]
		if current != nil && current.health.State == HealthReady {
			continue
		}
		if current == nil || current.health.State != HealthFailed || current.refs != 0 {
			failures = append(failures, attachmentFailure(key, current))
			continue
		}
		descriptor, desired := current.descriptor, current.desired
		if err := r.cleanupBoundedLocked(ctx, key, current); err != nil {
			failures = append(failures, attachmentFailure(key, current))
			continue
		}
		r.startLocked(ctx, key, descriptor)
		current = r.instances[key]
		current.desired = desired
		if current.health.State != HealthReady {
			failures = append(failures, attachmentFailure(key, current))
		}
	}
	if err := ctx.Err(); err != nil {
		failures = append(failures, err)
	}
	return errors.Join(failures...)
}
