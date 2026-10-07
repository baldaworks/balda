package scheduledjobs

import (
	"fmt"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/state"
	"go.uber.org/fx"
)

// Module wires the scheduled job application service.
var Module = fx.Module("balda_scheduled_jobs",
	fx.Provide(
		func(params scheduledJobSchedulerParams) (*ScheduledJobScheduler, error) {
			if params.JobStore == nil {
				return nil, fmt.Errorf("scheduled job store is required")
			}
			if params.RunStore == nil {
				return nil, fmt.Errorf("schedule run store is required")
			}
			if params.Dispatcher == nil {
				return nil, fmt.Errorf("balda actor dispatcher is required for scheduler")
			}
			config, err := normalizeScheduledJobSchedulerConfig(params.Config)
			if err != nil {
				return nil, err
			}
			if len(config.Jobs) > 0 && params.OwnerStore == nil && params.Resolver == nil {
				return nil, fmt.Errorf("balda destination resolver is required for scheduler jobs")
			}
			resolver := params.Resolver
			if resolver == nil && params.OwnerStore != nil {
				resolver = params.OwnerStore
			}

			scheduler := &ScheduledJobScheduler{
				jobStore:     params.JobStore,
				runStore:     params.RunStore,
				dispatcher:   params.Dispatcher,
				owner:        params.OwnerStore,
				resolver:     resolver,
				logger:       params.Logger.With().Str("component", "balda.scheduled_job_scheduler").Logger(),
				config:       config,
				pollInterval: defaultSchedulerPollInterval,
				dueBatchSize: defaultSchedulerDueBatchSize,
				now:          time.Now,
			}

			return scheduler, nil
		},
		fx.Annotate(func(s *ScheduledJobScheduler) appports.ScheduledJobRecorder { return s }),
		func(jobs state.ScheduledJobStore, store state.ScheduleManagementStore,
			runs state.ScheduleRunStore, executionJobs state.JobLifecycleStore,
			scheduler *ScheduledJobScheduler) *Management {
			return NewManagement(jobs, store, runs, executionJobs, scheduler.getResolver())
		},
	),
	fx.Invoke(func(*ScheduledJobScheduler) {}),
)
