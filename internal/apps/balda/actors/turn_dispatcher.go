package actors

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/appports"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turnstatuscmd"
	"github.com/rs/zerolog"
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
	recorder appports.TurnStatusRecorder
}

type sessionTurnQueue struct {
	pending        []*queuedTurn
	running        bool
	runningTurn    *queuedTurn
	startedAt      time.Time
	inFlightCancel context.CancelFunc
	wakeCh         chan struct{}
}

type queuedTurn struct {
	ctx          context.Context
	task         appports.TurnTask
	result       chan error
	constituents []*queuedTurn
}

func NewTurnDispatcher(logger zerolog.Logger) *TurnDispatcher {
	return NewTurnDispatcherWithStatus(logger, nil)
}

func NewTurnDispatcherWithStatus(logger zerolog.Logger, recorder appports.TurnStatusRecorder) *TurnDispatcher {
	dispatcher := &TurnDispatcher{
		logger:   logger.With().Str("component", "balda.turn_dispatcher").Logger(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
		recorder: recorder,
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

	incKey := turnDedupeKey(queued)
	if incKey != "" {
		if queue.running && queue.runningTurn != nil && batchContainsConstituent(queue.runningTurn, incKey) {
			queue.runningTurn.constituents = append(queue.runningTurn.constituents, queued)
			return queued.result, 0, nil
		}
		for i, pending := range queue.pending {
			if batchContainsConstituent(pending, incKey) {
				pending.constituents = append(pending.constituents, queued)
				pos := i
				if queue.running {
					pos = i + 1
				}
				return queued.result, pos, nil
			}
		}
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

// CurrentTurn returns the caller identity for the turn currently running in a session.
func (d *TurnDispatcher) CurrentTurn(sessionID string) (appports.CurrentTurn, bool) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return appports.CurrentTurn{}, false
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	queue := d.sessions[sessionID]
	if queue == nil || !queue.running || queue.runningTurn == nil || queue.runningTurn.task.SessionTurn == nil {
		return appports.CurrentTurn{}, false
	}

	payload := queue.runningTurn.task.SessionTurn
	return appports.CurrentTurn{
		SessionID:       sessionID,
		RequesterUserID: strings.TrimSpace(payload.RequesterUserID),
		Text:            payload.Text,
		StartedAt:       queue.startedAt,
		PendingTurns:    len(queue.pending),
	}, true
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
		if d.recorder != nil && isConversationTurn(turn.task.SessionTurn) {
			trackedCtx, trackErr := d.recorder.Begin(runCtx, sessionID, turn.task.SessionTurn.ArchiveID, queue.startedAt)
			if trackErr != nil {
				d.logger.Warn().Err(trackErr).Str("session_id", sessionID).Msg("record turn start")
			}
			runCtx = trackedCtx
		}
		if runCtx.Err() != nil {
			err = runCtx.Err()
		} else {
			err = turn.task.Run(runCtx)
		}
		if d.recorder != nil {
			outcome := turnstatuscmd.OutcomeSucceeded
			if errors.Is(err, context.Canceled) {
				outcome = turnstatuscmd.OutcomeCanceled
			} else if err != nil {
				outcome = turnstatuscmd.OutcomeFailed
			}
			if trackErr := d.recorder.Complete(context.WithoutCancel(runCtx), outcome, time.Now().UTC()); trackErr != nil {
				d.logger.Warn().Err(trackErr).Str("session_id", sessionID).Msg("record turn completion")
			}
		}
		cancel()

		d.mu.Lock()
		queue.running = false
		queue.runningTurn = nil
		queue.startedAt = time.Time{}
		queue.inFlightCancel = nil
		d.mu.Unlock()
		completeTurn(turn, err)

		if err != nil && !errors.Is(err, context.Canceled) {
			d.logger.Error().Err(err).Str("session_id", sessionID).Msg("turn task failed")
		}
	}
}

func isConversationTurn(payload *turncmd.SessionTurnPayload) bool {
	if payload == nil || strings.TrimSpace(payload.ArchiveID) == "" {
		return false
	}
	return payload.Source != turncmd.SourceSchedule && payload.Source != turncmd.SourceWebhook
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
			queue.startedAt = time.Now().UTC()
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
		if mergeIntoSteeringBatch(queue.pending[i], incoming) {
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
	if runningPayload.ArchiveID != incoming.ArchiveID || strings.TrimSpace(runningPayload.RequesterUserID) != strings.TrimSpace(incoming.RequesterUserID) {
		return false
	}
	return slices.Contains(steeringAnchorMessageIDs(runningPayload), incoming.ReplyToMessageID)
}

func turnDedupeKey(turn *queuedTurn) string {
	if turn == nil || turn.task.SessionTurn == nil {
		return ""
	}
	if key := strings.TrimSpace(turn.task.SessionTurn.DedupeKey); key != "" {
		return key
	}
	if turn.task.SessionTurn.MessageID > 0 {
		return fmt.Sprintf("msg:%d", turn.task.SessionTurn.MessageID)
	}
	return ""
}

func batchContainsConstituent(batch *queuedTurn, dedupeKey string) bool {
	if dedupeKey == "" || batch == nil {
		return false
	}
	if turnDedupeKey(batch) == dedupeKey {
		return true
	}
	for _, c := range batch.constituents {
		if turnDedupeKey(c) == dedupeKey {
			return true
		}
	}
	return false
}

func mergeIntoSteeringBatch(turn *queuedTurn, incoming *queuedTurn) bool {
	if turn == nil || turn.task.SessionTurn == nil || incoming == nil || incoming.task.SessionTurn == nil {
		return false
	}
	batch := turn.task.SessionTurn
	incomingPayload := incoming.task.SessionTurn
	if batch.ArchiveID != incomingPayload.ArchiveID || strings.TrimSpace(batch.RequesterUserID) != strings.TrimSpace(incomingPayload.RequesterUserID) {
		return false
	}

	incKey := turnDedupeKey(incoming)
	isDuplicate := incKey != "" && batchContainsConstituent(turn, incKey)

	if !isDuplicate {
		anchors := steeringAnchorMessageIDs(batch)
		matchesAnchor := slices.Contains(anchors, incomingPayload.ReplyToMessageID)
		matchesSameReply := batch.ReplyToMessageID > 0 && batch.ReplyToMessageID == incomingPayload.ReplyToMessageID
		if !matchesAnchor && !matchesSameReply {
			return false
		}
	}

	turn.constituents = append(turn.constituents, incoming)
	if turn.ctx.Err() != nil && incoming.ctx.Err() == nil {
		turn.ctx = incoming.ctx
	}

	if !isDuplicate {
		batch.SteeringMessages = append(batch.SteeringMessages, steeringMessageFromPayload(incomingPayload))
		batch.Attachments = append(batch.Attachments, incomingPayload.Attachments...)
		batch.Text = turncmd.RenderSteeringBatchText(batch.SteeringMessages)
		batch.MessageID = incomingPayload.MessageID
		batch.ReplyToMessageID = incomingPayload.ReplyToMessageID
		batch.Metadata = reconcileSessionTurnMetadata(batch.Metadata, incomingPayload.Metadata)
		if strings.TrimSpace(incomingPayload.ReceivedAt) != "" {
			batch.ReceivedAt = incomingPayload.ReceivedAt
		}
	}
	return true
}

func reconcileSessionTurnMetadata(current, incoming *turncmd.SessionTurnMetadata) *turncmd.SessionTurnMetadata {
	if current == nil || incoming == nil {
		return nil
	}
	currentMemoryAt := strings.TrimSpace(current.LatestMemoryAt)
	incomingMemoryAt := strings.TrimSpace(incoming.LatestMemoryAt)
	if currentMemoryAt == "" || currentMemoryAt != incomingMemoryAt {
		return nil
	}
	return current
}

func initializeSteeringBatch(payload *turncmd.SessionTurnPayload) *turncmd.SessionTurnPayload {
	if payload == nil {
		return nil
	}
	payload.SteeringMessages = []turncmd.SteeringMessage{steeringMessageFromPayload(payload)}
	payload.Text = turncmd.RenderSteeringBatchText(payload.SteeringMessages)
	return payload
}

func steeringMessageFromPayload(payload *turncmd.SessionTurnPayload) turncmd.SteeringMessage {
	if payload == nil {
		return turncmd.SteeringMessage{}
	}
	return turncmd.SteeringMessage{
		Attachments:      append([]attachment.Descriptor(nil), payload.Attachments...),
		ArchiveID:        payload.ArchiveID,
		DedupeKey:        payload.DedupeKey,
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

func completeTurn(turn *queuedTurn, err error) {
	if turn == nil {
		return
	}
	if turn.result != nil {
		turn.result <- err
		close(turn.result)
		turn.result = nil
	}
	for _, c := range turn.constituents {
		if c != nil && c.result != nil {
			c.result <- err
			close(c.result)
			c.result = nil
		}
	}
	turn.constituents = nil
}
