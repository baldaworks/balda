package runtime

import (
	"context"
	"strings"

	"github.com/normahq/balda/pkg/actorlayer"
	actorengine "github.com/normahq/balda/pkg/actorlayer/engine"
)

func (r *ActorHost) deadletterJob(ctx context.Context, env actorlayer.Envelope, reason string) {
	if r == nil || r.jobs == nil {
		return
	}
	jobID := strings.TrimSpace(env.TaskID)
	if jobID == "" {
		return
	}
	if err := r.jobs.DeadLetter(ctx, jobID, "runtime.host", env.ID, reason); err != nil {
		r.logger.Warn().Err(err).Str("task_id", jobID).Msg("failed to mark job deadlettered")
	}
}

func retryExhaustedDelivery(delivery actorengine.Delivery) bool {
	if delivery == nil {
		return false
	}
	return actorlayer.RetryExhausted(delivery.Attempt(), delivery.MaxAttempts())
}
