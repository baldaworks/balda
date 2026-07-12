package scheduledjobs

import (
	"fmt"
	"time"

	"github.com/normahq/balda/internal/apps/balda/appports"
	"go.uber.org/fx"
)

// Module wires the scheduled job application service.
var Module = fx.Module("balda_scheduled_jobs",
	fx.Provide(
		func(params scheduledJobSchedulerParams) (*ScheduledJobScheduler, error) {
			if params.JobStore == nil {
				return nil, fmt.Errorf("scheduled job store is required")
			}
			if params.Dispatcher == nil {
				return nil, fmt.Errorf("balda actor dispatcher is required for scheduler")
			}
			config, err := normalizeScheduledJobSchedulerConfig(params.Config)
			if err != nil {
				return nil, err
			}
			if len(config.Jobs) > 0 && params.OwnerStore == nil {
				return nil, fmt.Errorf("balda owner store is required for scheduler jobs")
			}

			scheduler := &ScheduledJobScheduler{
				jobStore:     params.JobStore,
				dispatcher:   params.Dispatcher,
				owner:        params.OwnerStore,
				logger:       params.Logger.With().Str("component", "balda.scheduled_job_scheduler").Logger(),
				config:       config,
				pollInterval: defaultSchedulerPollInterval,
				dueBatchSize: defaultSchedulerDueBatchSize,
				now:          time.Now,
			}

			return scheduler, nil
		},
		fx.Annotate(func(s *ScheduledJobScheduler) appports.ScheduledJobRecorder { return s }),
	),
	fx.Invoke(func(*ScheduledJobScheduler) {}),
)
