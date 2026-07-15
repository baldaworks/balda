package actors

import (
	"context"
	"fmt"
	"strings"

	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/actorcmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
)

type permissionDecisionSink interface {
	Resolve(reviewID, optionID string)
}

type permissionActor struct {
	sink permissionDecisionSink
}

func (a *permissionActor) Address() string {
	return actorlayer.WildcardAddress(actorcmd.ActorTypePermission)
}

func (a *permissionActor) Handle(_ context.Context, env actorlayer.Envelope) error {
	if strings.TrimSpace(env.Namespace) != actorcmd.NamespacePermissionCommand {
		return actorlayer.PolicyError(fmt.Errorf("unsupported permission namespace %q", env.Namespace))
	}
	if a.sink == nil {
		return actorlayer.TransientError(fmt.Errorf("permission decision sink is required"))
	}
	resumeTarget := ""
	optionID := ""
	switch strings.TrimSpace(env.Kind) {
	case actorcmd.KindQuestionAnswered:
		var payload questioncmd.AnsweredContinuation
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			return actorlayer.PermanentError(fmt.Errorf("decode permission answer: %w", err))
		}
		resumeTarget = payload.Resume.To
		optionID = payload.Answer.SelectedOption
	case actorcmd.KindQuestionTimedOut:
		var payload questioncmd.TimedOutContinuation
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			return actorlayer.PermanentError(fmt.Errorf("decode permission timeout: %w", err))
		}
		resumeTarget = payload.Resume.To
	default:
		return actorlayer.PolicyError(fmt.Errorf("unsupported permission kind %q", env.Kind))
	}
	resume, err := questioncmd.ParseResumeAddress(resumeTarget)
	if err != nil {
		return actorlayer.PermanentError(err)
	}
	if resume.Target != actorcmd.ActorTypePermission || strings.TrimSpace(resume.Key) != strings.TrimSpace(env.To.Key) {
		return actorlayer.PolicyError(fmt.Errorf("permission resume target mismatch"))
	}
	a.sink.Resolve(resume.Key, optionID)
	return nil
}
