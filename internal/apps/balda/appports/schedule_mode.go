package appports

import "context"

// ScheduleModeResolver classifies older queued schedule wires without a mode marker.
type ScheduleModeResolver interface {
	IsOneShot(ctx context.Context, scheduleID string) (bool, error)
}
