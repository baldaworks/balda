package deliverycmd

import (
	"fmt"
	"strings"

	baldaexecution "github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer"
)

type GoalProgressKind string

const (
	GoalProgressKindPlan      GoalProgressKind = "plan"
	GoalProgressKindOutput    GoalProgressKind = "output"
	GoalProgressKindCompleted GoalProgressKind = "completed"
)

type GoalProgressUpdate struct {
	JobID         string
	Locator       baldasession.SessionLocator
	Profile       deliveryfmt.Profile
	Policy        deliveryfmt.ProgressPolicy
	Step          string
	Iteration     int
	MaxIterations int
	Kind          GoalProgressKind
	Text          string
	Plan          *progress.PlanSnapshot
	Sequence      int
}

func GoalProgressEnvelope(update GoalProgressUpdate) (actorlayer.Envelope, error) {
	from := actorlayer.ActorAddress{Target: baldaexecution.ActorTypeGoalkeeper, Key: strings.TrimSpace(update.JobID)}
	switch update.Kind {
	case GoalProgressKindPlan:
		message := strings.TrimSpace(update.Text)
		if message == "" {
			return actorlayer.Envelope{}, nil
		}
		return ProgressPlanUpdateEnvelope(
			strings.TrimSpace(update.JobID),
			from,
			update.Locator,
			update.Policy,
			update.Policy.PlanUpdates,
			update.Plan,
			message,
			goalProgressDedupeSuffix(update),
		)
	case GoalProgressKindOutput, GoalProgressKindCompleted:
		message := strings.TrimSpace(update.Text)
		if message == "" {
			return actorlayer.Envelope{}, nil
		}
		return AgentReplyEnvelopeWithProfile(
			strings.TrimSpace(update.JobID),
			from,
			update.Locator,
			update.Profile,
			message,
			goalProgressDedupeSuffix(update),
		)
	default:
		return actorlayer.Envelope{}, fmt.Errorf("unsupported goal progress kind %q", update.Kind)
	}
}

func goalProgressDedupeSuffix(update GoalProgressUpdate) string {
	iteration := update.Iteration
	if iteration <= 0 {
		iteration = 1
	}
	return fmt.Sprintf("progress:%s:%s:%d:%03d", update.Kind, strings.TrimSpace(update.Step), iteration, update.Sequence)
}
