package handlers

import (
	"context"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
)

type sessionProgressEmitter interface {
	HandleNonTerminal(ctx context.Context, plan progress.PlanSnapshot, planProgressText string, hasPlanUpdate bool, reasoningText string, hasThoughtUpdate bool, hasVisibleResponseText bool)
}

type sessionProgressDispatcher struct {
	dispatcher actortransport.Dispatcher
	from       actorlayer.ActorAddress
	locator    baldasession.SessionLocator
	draftID    int
	topicID    int
	policy     deliveryfmt.ProgressPolicy
	logger     zerolog.Logger

	thinkingIdx     int
	lastPlanText    string
	planDraftActive bool
}

func newSessionProgressDispatcher(
	dispatcher actortransport.Dispatcher,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	draftID int,
	topicID int,
	policy deliveryfmt.ProgressPolicy,
	logger zerolog.Logger,
) sessionProgressEmitter {
	return &sessionProgressDispatcher{
		dispatcher: dispatcher,
		from:       from,
		locator:    locator,
		draftID:    draftID,
		topicID:    topicID,
		policy:     policy,
		logger:     logger,
	}
}

func (d *sessionProgressDispatcher) HandleNonTerminal(ctx context.Context, plan progress.PlanSnapshot, planProgressText string, hasPlanUpdate bool, reasoningText string, hasThoughtUpdate bool, hasVisibleResponseText bool) {
	if d == nil {
		return
	}
	if hasPlanUpdate && planProgressText != "" && planProgressText != d.lastPlanText {
		visiblePlanUpdate := d.policy.PlanUpdates
		if err := sendProgressPlanUpdate(ctx, d.dispatcher, "", d.from, d.locator, d.policy, visiblePlanUpdate, d.draftID, &plan, planProgressText, ""); err != nil {
			d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to dispatch plan progress delivery")
		} else {
			d.lastPlanText = planProgressText
			if d.policy.Thinking {
				d.planDraftActive = true
			}
		}
	}
	if d.policy.Thinking && hasThoughtUpdate && !d.planDraftActive && !hasVisibleResponseText {
		if err := sendProgressThinking(ctx, d.dispatcher, "", d.from, d.locator, d.policy, true, d.draftID, reasoningText, d.thinkingIdx, ""); err != nil {
			d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to dispatch thinking progress delivery")
		} else {
			d.thinkingIdx++
		}
	}
}
