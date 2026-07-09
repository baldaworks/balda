package runtime

import (
	"context"
	"strings"

	"github.com/normahq/balda/pkg/actorlayer"
	actorengine "github.com/normahq/balda/pkg/actorlayer/engine"
)

func (r *ActorHost) deadletterTask(ctx context.Context, env actorlayer.Envelope, reason string) {
	if r == nil || r.tasks == nil {
		return
	}
	taskID := strings.TrimSpace(env.TaskID)
	if taskID == "" {
		return
	}
	if err := r.tasks.DeadLetter(ctx, taskID, "runtime.host", env.ID, reason); err != nil {
		r.logger.Warn().Err(err).Str("task_id", taskID).Msg("failed to mark job deadlettered")
	}
}

func retryExhaustedDelivery(delivery actorengine.Delivery) bool {
	if delivery == nil {
		return false
	}
	return actorlayer.RetryExhausted(delivery.Attempt(), delivery.MaxAttempts())
}
