package handlers

import (
	"context"
	"fmt"
	"strings"

	"github.com/normahq/balda/internal/apps/balda/deliveryfmt"
	"github.com/normahq/balda/internal/apps/balda/progress"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
)

type sessionProgressEmitter interface {
	HandleNonTerminal(ctx context.Context, update sessionProgressUpdate) (sessionProgressResult, error)
}

type sessionProgressDispatcher struct {
	dispatcher actortransport.Dispatcher
	from       actorlayer.ActorAddress
	locator    baldasession.SessionLocator
	jobID      string
	draftID    int
	topicID    int
	policy     deliveryfmt.ProgressPolicy
	logger     zerolog.Logger
	failHard   bool

	thinkingIdx     int
	lastPlanText    string
	planDraftActive bool
	deliverySeq     int
}

type sessionProgressUpdate struct {
	Plan                   progress.PlanSnapshot
	PlanProgressText       string
	HasPlanUpdate          bool
	ReasoningText          string
	HasThoughtUpdate       bool
	HasVisibleResponseText bool
}

type sessionProgressResult struct {
	SentProgress       bool
	DispatchedPlanText string
}

func newSessionProgressDispatcher(
	dispatcher actortransport.Dispatcher,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	jobID string,
	draftID int,
	topicID int,
	policy deliveryfmt.ProgressPolicy,
	failHard bool,
	logger zerolog.Logger,
) sessionProgressEmitter {
	return &sessionProgressDispatcher{
		dispatcher: dispatcher,
		from:       from,
		locator:    locator,
		jobID:      jobID,
		draftID:    draftID,
		topicID:    topicID,
		policy:     policy,
		logger:     logger,
		failHard:   failHard,
	}
}

func (d *sessionProgressDispatcher) HandleNonTerminal(ctx context.Context, update sessionProgressUpdate) (sessionProgressResult, error) {
	if d == nil {
		return sessionProgressResult{}, nil
	}
	result := sessionProgressResult{}
	if update.HasPlanUpdate && update.PlanProgressText != "" && update.PlanProgressText != d.lastPlanText {
		visiblePlanUpdate := d.policy.PlanUpdates
		d.deliverySeq++
		dedupeSuffix := fmt.Sprintf("progress:plan:%03d", d.deliverySeq)
		if err := sendProgressPlanUpdate(ctx, d.dispatcher, d.jobID, d.from, d.locator, d.policy, visiblePlanUpdate, d.draftID, &update.Plan, update.PlanProgressText, dedupeSuffix); err != nil {
			if dispatchErr := d.handleDispatchError(err, "failed to dispatch plan progress delivery"); dispatchErr != nil {
				return result, dispatchErr
			}
		} else {
			d.lastPlanText = update.PlanProgressText
			result.DispatchedPlanText = strings.TrimSpace(update.PlanProgressText)
			if visiblePlanUpdate {
				d.planDraftActive = true
			}
			result.SentProgress = true
		}
	}
	if update.HasThoughtUpdate {
		visibleThinking := d.policy.Thinking && strings.TrimSpace(update.ReasoningText) != "" && !d.planDraftActive && !update.HasVisibleResponseText
		d.deliverySeq++
		dedupeSuffix := fmt.Sprintf("progress:thinking:%03d", d.deliverySeq)
		if err := sendProgressThinking(ctx, d.dispatcher, d.jobID, d.from, d.locator, d.policy, visibleThinking, d.draftID, update.ReasoningText, d.thinkingIdx, dedupeSuffix); err != nil {
			if dispatchErr := d.handleDispatchError(err, "failed to dispatch thinking progress delivery"); dispatchErr != nil {
				return result, dispatchErr
			}
		} else {
			if visibleThinking {
				d.thinkingIdx++
			}
			result.SentProgress = true
		}
	}
	if result.SentProgress {
		return result, nil
	}
	d.deliverySeq++
	dedupeSuffix := fmt.Sprintf("progress:activity:%03d", d.deliverySeq)
	if err := sendProgressActivity(ctx, d.dispatcher, d.jobID, d.from, d.locator, d.policy, d.deliverySeq, dedupeSuffix); err != nil {
		if dispatchErr := d.handleDispatchError(err, "failed to dispatch activity progress delivery"); dispatchErr != nil {
			return result, dispatchErr
		}
	}
	return result, nil
}

func (d *sessionProgressDispatcher) handleDispatchError(err error, msg string) error {
	if d.failHard {
		return err
	}
	d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg(msg)
	return nil
}
