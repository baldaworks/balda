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
		NewRunFinalizer,
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
				finalizer:    params.Finalizer,
				logger:       params.Logger.With().Str("component", "balda.scheduled_job_scheduler").Logger(),
				config:       config,
				pollInterval: defaultSchedulerPollInterval,
				dueBatchSize: defaultSchedulerDueBatchSize,
				now:          time.Now,
			}

			return scheduler, nil
		},
		fx.Annotate(func(s *ScheduledJobScheduler) appports.ScheduledJobRecorder { return s }),
		fx.Annotate(func(jobs state.ScheduledJobStore) appports.ScheduleModeResolver {
			return modeResolver{jobs: jobs}
		}),
		func(jobs state.ScheduledJobStore, store state.ScheduleManagementStore,
			runs state.ScheduleRunStore, executionJobs state.JobLifecycleStore,
			deliveries state.DeliveryStore,
			scheduler *ScheduledJobScheduler) *Management {
			management := NewManagement(jobs, store, runs, executionJobs, deliveries)
			management.resolver = scheduler.resolver
			return management
		},
	),
	fx.Invoke(func(*ScheduledJobScheduler) {}),
)
