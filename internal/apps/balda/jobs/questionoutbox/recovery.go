// Package questionoutbox recovers settled answers independently of inbound delivery.
package questionoutbox

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

// Source retains the answer and its recovery marker in the same SQL mutation.
type Source interface {
	PendingQuestionAnswerIDs(ctx context.Context, afterID string, limit int) ([]string, error)
	AnsweredContinuation(ctx context.Context, questionID string) (actorlayer.Envelope, bool, error)
	ConfirmAnswerAdmission(ctx context.Context, questionID string) error
}

// Admitter returns a receipt only after the continuation is locally durable.
type Admitter interface {
	Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error)
}

// Runner advances a bounded cursor so one invalid answer cannot starve others.
type Runner struct {
	source   Source
	admitter Admitter
	logger   zerolog.Logger
	flushMu  sync.Mutex
	cursor   string
	mu       sync.Mutex
	cancel   context.CancelFunc
	done     chan struct{}
}

func New(source Source, admitter Admitter, logger zerolog.Logger) (*Runner, error) {
	if source == nil || admitter == nil {
		return nil, fmt.Errorf("question recovery requires source and durable admission")
	}
	return &Runner{source: source, admitter: admitter, logger: logger}, nil
}

// Flush admits persisted answers before releasing their recovery marker/controls.
func (r *Runner) Flush(ctx context.Context) error {
	r.flushMu.Lock()
	defer r.flushMu.Unlock()
	const batch = 32
	ids, err := r.source.PendingQuestionAnswerIDs(ctx, r.cursor, batch)
	if err != nil {
		return err
	}
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.cursor = id
		if err := r.admit(ctx, id); err != nil {
			failures = append(failures, fmt.Errorf("question %s: %w", id, err))
		}
	}
	if len(ids) < batch {
		r.cursor = ""
	}
	return errors.Join(failures...)
}

func (r *Runner) admit(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	env, found, err := r.source.AnsweredContinuation(ctx, id)
	if err != nil {
		return err
	}
	if found {
		receipt, err := r.admitter.Dispatch(ctx, env)
		if err != nil {
			return err
		}
		if receipt == nil {
			return fmt.Errorf("question continuation admission returned no receipt")
		}
	}
	return r.source.ConfirmAnswerAdmission(ctx, id)
}

func (r *Runner) Start() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	done := r.done
	go func() {
		defer close(done)
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			if err := r.Flush(ctx); err != nil && ctx.Err() == nil {
				r.logger.Warn().Err(err).Msg("recover pending question answers")
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (r *Runner) Stop(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel == nil {
		return nil
	}
	r.cancel()
	select {
	case <-r.done:
		r.cancel = nil
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
