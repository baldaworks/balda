package scheduledjobs

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/balda/internal/apps/balda/state"
)

type modeResolver struct{ jobs state.ScheduledJobStore }

func (r modeResolver) IsOneShot(ctx context.Context, scheduleID string) (bool, error) {
	if strings.TrimSpace(scheduleID) == "" {
		return false, fmt.Errorf("schedule id is required")
	}
	job, found, err := r.jobs.GetByID(ctx, scheduleID)
	if err != nil {
		return false, fmt.Errorf("load schedule mode: %w", err)
	}
	if !found {
		// Startup archived config/managed definitions, but older releases may
		// have removed internal timers before an already-published command replayed.
		return true, nil
	}
	return job.Source == state.ScheduledJobSourceInternal || isOneShotScheduleSpec(job.ScheduleSpec), nil
}
