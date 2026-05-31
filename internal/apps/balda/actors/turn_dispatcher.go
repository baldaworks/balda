package actors

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/normahq/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	perSessionQueueLimit = 20
	sessionWorkerIdleTTL = 5 * time.Minute
)

var ErrTurnQueueFull = errors.New("turn queue is full")

type TurnTask struct {
	SessionID string
	Context   context.Context
	Run       func(context.Context) error
}

type TurnQueue interface {
	Enqueue(task TurnTask) (int, error)
	CancelSession(locator session.SessionLocator, clearQueued bool) (bool, int, error)
}

type TurnDispatcher struct {
	logger zerolog.Logger

	mu       sync.Mutex
	sessions map[string]*sessionTurnQueue
	stopping bool
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type sessionTurnQueue struct {
	pending        []TurnTask
	running        bool
	inFlightCancel context.CancelFunc
	wakeCh         chan struct{}
}

type turnDispatcherParams struct {
	fx.In

	LC     fx.Lifecycle
	Logger zerolog.Logger
}

func NewTurnDispatcher(params turnDispatcherParams) *TurnDispatcher {
	dispatcher := &TurnDispatcher{
		logger:   params.Logger.With().Str("component", "balda.turn_dispatcher").Logger(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	params.LC.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			return dispatcher.Shutdown(ctx)
		},
	})
	return dispatcher
}

func (d *TurnDispatcher) Enqueue(task TurnTask) (int, error) {
	sessionID := strings.TrimSpace(task.SessionID)
	if sessionID == "" {
		return 0, fmt.Errorf("session id is required")
	}
	if task.Run == nil {
		return 0, fmt.Errorf("turn task runner is required")
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return 0, fmt.Errorf("turn dispatcher is stopping")
	}

	queue, ok := d.sessions[sessionID]
	if !ok {
		queue = &sessionTurnQueue{
			wakeCh: make(chan struct{}, 1),
		}
		d.sessions[sessionID] = queue
		d.wg.Add(1)
		go d.sessionWorker(sessionID, queue)
	}

	pendingBefore := len(queue.pending)
	if pendingBefore >= perSessionQueueLimit {
		return 0, ErrTurnQueueFull
	}

	position := 0
	if queue.running {
		position = pendingBefore + 1
	} else if pendingBefore > 0 {
		position = pendingBefore
	}

	queue.pending = append(queue.pending, task)
	select {
	case queue.wakeCh <- struct{}{}:
	default:
	}

	return position, nil
}

func (d *TurnDispatcher) CancelSession(locator session.SessionLocator, clearQueued bool) (bool, int, error) {
	sessionID := strings.TrimSpace(locator.SessionID)
	if sessionID == "" {
		return false, 0, fmt.Errorf("session id is required")
	}

	d.mu.Lock()
	queue := d.sessions[sessionID]
	if queue == nil {
		d.mu.Unlock()
		return false, 0, nil
	}

	dropped := 0
	if clearQueued {
		dropped = len(queue.pending)
		queue.pending = nil
	}

	cancel := queue.inFlightCancel
	hadInFlight := cancel != nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}

	return hadInFlight, dropped, nil
}

func (d *TurnDispatcher) Shutdown(ctx context.Context) error {
	d.mu.Lock()
	if d.stopping {
		d.mu.Unlock()
		return nil
	}
	d.stopping = true
	close(d.stopCh)
	cancels := make([]context.CancelFunc, 0, len(d.sessions))
	for _, queue := range d.sessions {
		if queue != nil && queue.inFlightCancel != nil {
			cancels = append(cancels, queue.inFlightCancel)
		}
	}
	d.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		d.wg.Wait()
	}()

	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (d *TurnDispatcher) sessionWorker(sessionID string, queue *sessionTurnQueue) {
	defer d.wg.Done()

	for {
		task, ok := d.nextTask(sessionID, queue)
		if !ok {
			return
		}

		baseCtx := task.Context
		if baseCtx == nil {
			baseCtx = context.Background()
		}
		runCtx, cancel := context.WithCancel(baseCtx)
		d.mu.Lock()
		queue.running = true
		queue.inFlightCancel = cancel
		d.mu.Unlock()

		var err error
		if runCtx.Err() != nil {
			err = runCtx.Err()
		} else {
			err = task.Run(runCtx)
		}
		cancel()

		d.mu.Lock()
		queue.running = false
		queue.inFlightCancel = nil
		d.mu.Unlock()

		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Error().Err(err).Str("session_id", sessionID).Msg("turn task failed")
		}
	}
}

func (d *TurnDispatcher) nextTask(sessionID string, queue *sessionTurnQueue) (TurnTask, bool) {
	for {
		d.mu.Lock()
		if d.stopping {
			delete(d.sessions, sessionID)
			d.mu.Unlock()
			return TurnTask{}, false
		}
		if len(queue.pending) > 0 {
			task := queue.pending[0]
			queue.pending = queue.pending[1:]
			d.mu.Unlock()
			return task, true
		}
		d.mu.Unlock()

		idleTimer := time.NewTimer(sessionWorkerIdleTTL)
		select {
		case <-d.stopCh:
			if !idleTimer.Stop() {
				<-idleTimer.C
			}
			d.mu.Lock()
			delete(d.sessions, sessionID)
			d.mu.Unlock()
			return TurnTask{}, false
		case <-queue.wakeCh:
			if !idleTimer.Stop() {
				<-idleTimer.C
			}
			continue
		case <-idleTimer.C:
			d.mu.Lock()
			if d.sessions[sessionID] == queue && !queue.running && len(queue.pending) == 0 {
				delete(d.sessions, sessionID)
				d.mu.Unlock()
				return TurnTask{}, false
			}
			d.mu.Unlock()
			continue
		}
	}
}
