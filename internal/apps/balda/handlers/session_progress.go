package handlers

import (
	"context"
	"time"

	baldachannel "github.com/normahq/balda/internal/apps/balda/channel"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/throttle"
	"github.com/normahq/balda/pkg/actorlayer"
	actortransport "github.com/normahq/balda/pkg/actorlayer/transport"
	"github.com/rs/zerolog"
)

type sessionProgressEmitter interface {
	HandleNonTerminal(ctx context.Context, planProgressText string, hasPlanUpdate bool)
}

type sessionProgressDispatcher struct {
	dispatcher actortransport.Dispatcher
	from       actorlayer.ActorAddress
	locator    baldasession.SessionLocator
	draftID    int
	topicID    int
	policy     baldachannel.ProgressPolicy
	logger     zerolog.Logger

	typingThrottle   *throttle.Throttler
	thinkingThrottle *throttle.Throttler
	thinkingIdx      int
	lastPlanText     string
	planDraftActive  bool
}

func newSessionProgressDispatcher(
	dispatcher actortransport.Dispatcher,
	from actorlayer.ActorAddress,
	locator baldasession.SessionLocator,
	draftID int,
	topicID int,
	policy baldachannel.ProgressPolicy,
	logger zerolog.Logger,
	now func() time.Time,
) sessionProgressEmitter {
	return &sessionProgressDispatcher{
		dispatcher:       dispatcher,
		from:             from,
		locator:          locator,
		draftID:          draftID,
		topicID:          topicID,
		policy:           policy,
		logger:           logger,
		typingThrottle:   throttle.New(telegramProgressThrottleInterval, throttle.WithClock(now)),
		thinkingThrottle: throttle.New(telegramProgressThrottleInterval, throttle.WithClock(now)),
	}
}

var sessionThinkingStages = []string{"Thinking.", "Thinking..", "Thinking..."}

func (d *sessionProgressDispatcher) HandleNonTerminal(ctx context.Context, planProgressText string, hasPlanUpdate bool) {
	if d == nil {
		return
	}
	if d.policy.Typing {
		d.typingThrottle.Do(func() {
			if err := sendTyping(ctx, d.dispatcher, d.from, d.locator); err != nil {
				d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to send typing chat action")
			}
		})
	}
	if hasPlanUpdate && planProgressText != "" && planProgressText != d.lastPlanText {
		if d.policy.Thinking {
			if err := sendDraftPlain(ctx, d.dispatcher, d.from, d.locator, d.draftID, planProgressText); err != nil {
				d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to send plan update placeholder")
			} else {
				d.lastPlanText = planProgressText
				d.planDraftActive = true
			}
		} else {
			if err := sendPlain(ctx, d.dispatcher, d.from, d.locator, planProgressText); err != nil {
				d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to send plan update message")
			} else {
				d.lastPlanText = planProgressText
			}
		}
	}
	if d.policy.Thinking && !d.planDraftActive {
		d.thinkingThrottle.Do(func() {
			text := sessionThinkingStages[d.thinkingIdx%len(sessionThinkingStages)]
			if err := sendDraftPlain(ctx, d.dispatcher, d.from, d.locator, d.draftID, text); err != nil {
				d.logger.Warn().Err(err).Int("topic_id", d.topicID).Msg("failed to send thinking placeholder")
			}
			d.thinkingIdx++
		})
	}
}
