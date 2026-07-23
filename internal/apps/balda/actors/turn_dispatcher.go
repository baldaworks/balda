package actors

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/normahq/balda/internal/apps/balda/appports"
	"github.com/normahq/balda/internal/apps/balda/session"
	"github.com/normahq/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
	"go.uber.org/fx"
)

const (
	perSessionQueueLimit = 20
	sessionWorkerIdleTTL = 5 * time.Minute
)

var ErrTurnQueueFull = errors.New("turn queue is full")

type TurnTask = appports.TurnTask
type TurnQueue = appports.TurnQueue

type TurnDispatcher struct {
	logger zerolog.Logger

	mu       sync.Mutex
	sessions map[string]*sessionTurnQueue
	stopping bool
	stopCh   chan struct{}
	wg       sync.WaitGroup
}

type sessionTurnQueue struct {
	pending        []*queuedTurn
	running        bool
	runningTurn    *queuedTurn
	inFlightCancel context.CancelFunc
	wakeCh         chan struct{}
}

type queuedTurn struct {
	ctx    context.Context
	task   appports.TurnTask
	result chan error
}

type turnDispatcherParams struct {
	fx.In

	Logger zerolog.Logger
}

func NewTurnDispatcher(params turnDispatcherParams) *TurnDispatcher {
	dispatcher := &TurnDispatcher{
		logger:   params.Logger.With().Str("component", "balda.turn_dispatcher").Logger(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	return dispatcher
}

func (d *TurnDispatcher) Enqueue(ctx context.Context, task appports.TurnTask) (<-chan error, int, error) {
	sessionID := strings.TrimSpace(task.SessionID)
	if sessionID == "" {
		return nil, 0, fmt.Errorf("session id is required")
	}
	if task.Run == nil {
		return nil, 0, fmt.Errorf("turn task runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return nil, 0, fmt.Errorf("turn dispatcher is stopping")
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
		return nil, 0, ErrTurnQueueFull
	}

	queued := &queuedTurn{
		ctx:    ctx,
		task:   task,
		result: make(chan error, 1),
	}
	if queue.running && tryMergeSteeringTurn(queue, queued) {
		return queued.result, 1, nil
	}

	position := 0
	if queue.running {
		position = len(queue.pending) + 1
	} else if len(queue.pending) > 0 {
		position = len(queue.pending)
	}
	queue.pending = append(queue.pending, queued)
	select {
	case queue.wakeCh <- struct{}{}:
	default:
	}

	return queued.result, position, nil
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

	var droppedTurns []*queuedTurn
	if clearQueued {
		droppedTurns = queue.pending
		queue.pending = nil
	}

	cancel := queue.inFlightCancel
	hadInFlight := cancel != nil
	d.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	completeTurns(droppedTurns, context.Canceled)

	return hadInFlight, len(droppedTurns), nil
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
	droppedTurns := make([]*queuedTurn, 0)
	for _, queue := range d.sessions {
		if queue == nil {
			continue
		}
		if queue.inFlightCancel != nil {
			cancels = append(cancels, queue.inFlightCancel)
		}
		droppedTurns = append(droppedTurns, queue.pending...)
		queue.pending = nil
	}
	d.mu.Unlock()

	for _, cancel := range cancels {
		cancel()
	}
	completeTurns(droppedTurns, context.Canceled)

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
		turn, runCtx, cancel, ok := d.nextTask(sessionID, queue)
		if !ok {
			return
		}

		var err error
		if runCtx.Err() != nil {
			err = runCtx.Err()
		} else {
			err = turn.task.Run(runCtx)
		}
		cancel()

		d.mu.Lock()
		queue.running = false
		queue.runningTurn = nil
		queue.inFlightCancel = nil
		d.mu.Unlock()
		completeTurn(turn, err)

		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Error().Err(err).Str("session_id", sessionID).Msg("turn task failed")
		}
	}
}

func (d *TurnDispatcher) nextTask(
	sessionID string,
	queue *sessionTurnQueue,
) (*queuedTurn, context.Context, context.CancelFunc, bool) {
	for {
		d.mu.Lock()
		if d.stopping {
			delete(d.sessions, sessionID)
			d.mu.Unlock()
			return nil, nil, nil, false
		}
		if len(queue.pending) > 0 {
			turn := queue.pending[0]
			queue.pending = queue.pending[1:]
			runCtx, cancel := context.WithCancel(turn.ctx)
			queue.running = true
			queue.runningTurn = turn
			queue.inFlightCancel = cancel
			d.mu.Unlock()
			return turn, runCtx, cancel, true
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
			return nil, nil, nil, false
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
				return nil, nil, nil, false
			}
			d.mu.Unlock()
			continue
		}
	}
}

func completeTurns(turns []*queuedTurn, err error) {
	for _, turn := range turns {
		completeTurn(turn, err)
	}
}

func tryMergeSteeringTurn(queue *sessionTurnQueue, incoming *queuedTurn) bool {
	if queue == nil || incoming == nil || incoming.task.SessionTurn == nil {
		return false
	}
	if !isSteeringCandidate(incoming.task.SessionTurn) {
		return false
	}
	for i := len(queue.pending) - 1; i >= 0; i-- {
		if mergeIntoSteeringBatch(queue.pending[i], incoming.task.SessionTurn) {
			completeTurn(incoming, nil)
			return true
		}
	}
	runningPayload := (*turncmd.SessionTurnPayload)(nil)
	if queue.runningTurn != nil {
		runningPayload = queue.runningTurn.task.SessionTurn
	}
	if !matchesRunningSteering(runningPayload, incoming.task.SessionTurn) {
		return false
	}
	incoming.task.SessionTurn = initializeSteeringBatch(incoming.task.SessionTurn)
	return false
}

func isSteeringCandidate(payload *turncmd.SessionTurnPayload) bool {
	if payload == nil {
		return false
	}
	return payload.MessageID > 0 && payload.ReplyToMessageID > 0 && strings.TrimSpace(payload.RequesterUserID) != ""
}

func matchesRunningSteering(runningPayload, incoming *turncmd.SessionTurnPayload) bool {
	if runningPayload == nil || incoming == nil {
		return false
	}
	if strings.TrimSpace(runningPayload.RequesterUserID) == "" || strings.TrimSpace(incoming.RequesterUserID) == "" {
		return false
	}
	if strings.TrimSpace(runningPayload.RequesterUserID) != strings.TrimSpace(incoming.RequesterUserID) {
		return false
	}
	return slices.Contains(steeringAnchorMessageIDs(runningPayload), incoming.ReplyToMessageID)
}

func mergeIntoSteeringBatch(turn *queuedTurn, incoming *turncmd.SessionTurnPayload) bool {
	if turn == nil || turn.task.SessionTurn == nil || incoming == nil {
		return false
	}
	batch := turn.task.SessionTurn
	if strings.TrimSpace(batch.RequesterUserID) != strings.TrimSpace(incoming.RequesterUserID) {
		return false
	}
	if !slices.Contains(steeringAnchorMessageIDs(batch), incoming.ReplyToMessageID) {
		return false
	}
	batch.SteeringMessages = append(batch.SteeringMessages, steeringMessageFromPayload(incoming))
	batch.Text = renderSteeringBatchText(batch.SteeringMessages)
	batch.MessageID = incoming.MessageID
	batch.ReplyToMessageID = incoming.ReplyToMessageID
	if strings.TrimSpace(incoming.ReceivedAt) != "" {
		batch.ReceivedAt = incoming.ReceivedAt
	}
	return true
}

func initializeSteeringBatch(payload *turncmd.SessionTurnPayload) *turncmd.SessionTurnPayload {
	if payload == nil {
		return nil
	}
	payload.SteeringMessages = []turncmd.SteeringMessage{steeringMessageFromPayload(payload)}
	payload.Text = renderSteeringBatchText(payload.SteeringMessages)
	return payload
}

func steeringMessageFromPayload(payload *turncmd.SessionTurnPayload) turncmd.SteeringMessage {
	if payload == nil {
		return turncmd.SteeringMessage{}
	}
	return turncmd.SteeringMessage{
		MessageID:        payload.MessageID,
		ReplyToMessageID: payload.ReplyToMessageID,
		UserID:           strings.TrimSpace(payload.RequesterUserID),
		ReceivedAt:       strings.TrimSpace(payload.ReceivedAt),
		Text:             strings.TrimSpace(payload.Text),
	}
}

func steeringAnchorMessageIDs(payload *turncmd.SessionTurnPayload) []int {
	if payload == nil {
		return nil
	}
	ids := make([]int, 0, 1+len(payload.SteeringMessages))
	if payload.MessageID > 0 {
		ids = append(ids, payload.MessageID)
	}
	for _, msg := range payload.SteeringMessages {
		if msg.MessageID > 0 && !slices.Contains(ids, msg.MessageID) {
			ids = append(ids, msg.MessageID)
		}
	}
	return ids
}

func renderSteeringBatchText(messages []turncmd.SteeringMessage) string {
	if len(messages) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("Steering messages:\n")
	for i, msg := range messages {
		if i > 0 {
			b.WriteByte('\n')
		}
		ts := strings.TrimSpace(msg.ReceivedAt)
		if ts == "" {
			ts = "unknown_time"
		}
		b.WriteString("- [")
		b.WriteString(ts)
		b.WriteString("] ")
		b.WriteString(strings.TrimSpace(msg.Text))
	}
	return b.String()
}

func completeTurn(turn *queuedTurn, err error) {
	if turn == nil {
		return
	}
	turn.result <- err
	close(turn.result)
}
