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
	perSessionQueueLimit     = 20
	terminalReplacementLimit = 32
	sessionWorkerIdleTTL     = 5 * time.Minute
)

var ErrTurnQueueFull = appports.ErrTurnQueueFull

var (
	ErrTurnReplacementPending  = appports.ErrTurnReplacementPending
	ErrTurnReplacementConflict = appports.ErrTurnReplacementConflict
	ErrTurnReplacementFailed   = appports.ErrTurnReplacementFailed
)

type TurnTask = appports.TurnTask
type TurnQueue = appports.TurnQueue
type ReplacementTask = appports.ReplacementTask

var _ appports.ReplacementQueue = (*TurnDispatcher)(nil)

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
	pending              []*queuedTurn
	running              bool
	runningTurn          *queuedTurn
	startedAt            time.Time
	inFlightCancel       context.CancelFunc
	wakeCh               chan struct{}
	replacement          *queuedTurn
	terminalReplacements map[string]replacementResult
	terminalOrder        []string
}

type replacementResult struct {
	inputHash string
	err       error
}

type queuedTurn struct {
	ctx          context.Context
	task         appports.TurnTask
	result       chan error
	constituents []*queuedTurn
	replacement  *appports.ReplacementTask
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
	if queue.replacement != nil {
		return nil, 0, ErrTurnReplacementPending
	}
	if len(queue.pending) >= perSessionQueueLimit {
		return nil, 0, ErrTurnQueueFull
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

// EnqueueReplacement appends one exclusive replacement boundary to the
// session's existing FIFO. Work accepted earlier completes first; ordinary
// work admitted after the boundary is rejected until it completes.
func (d *TurnDispatcher) EnqueueReplacement(ctx context.Context, task ReplacementTask) (<-chan error, int, error) {
	task.SessionID = strings.TrimSpace(task.SessionID)
	task.OperationID = strings.TrimSpace(task.OperationID)
	task.InputHash = strings.TrimSpace(task.InputHash)
	if task.SessionID == "" {
		return nil, 0, fmt.Errorf("session id is required")
	}
	if task.OperationID == "" {
		return nil, 0, fmt.Errorf("replacement operation id is required")
	}
	if task.InputHash == "" {
		return nil, 0, fmt.Errorf("replacement input hash is required")
	}
	if task.Run == nil {
		return nil, 0, fmt.Errorf("replacement runner is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopping {
		return nil, 0, fmt.Errorf("turn dispatcher is stopping")
	}

	queue, ok := d.sessions[task.SessionID]
	if !ok {
		queue = &sessionTurnQueue{wakeCh: make(chan struct{}, 1)}
		d.sessions[task.SessionID] = queue
		d.wg.Add(1)
		go d.sessionWorker(task.SessionID, queue)
	}
	if terminal, ok := queue.terminalReplacements[task.OperationID]; ok {
		if terminal.inputHash != task.InputHash {
			return nil, 0, ErrTurnReplacementConflict
		}
		return completedTurnResult(terminal.err), 0, nil
	}
	if queue.replacement != nil {
		if queue.replacement.replacement.OperationID != task.OperationID {
			return nil, 0, ErrTurnReplacementPending
		}
		if queue.replacement.replacement.InputHash != task.InputHash {
			return nil, 0, ErrTurnReplacementConflict
		}
		duplicate := &queuedTurn{ctx: ctx, result: make(chan error, 1)}
		queue.replacement.constituents = append(queue.replacement.constituents, duplicate)
		return duplicate.result, queuePosition(queue, queue.replacement), nil
	}
	if len(queue.pending) >= perSessionQueueLimit {
		return nil, 0, ErrTurnQueueFull
	}

	queued := &queuedTurn{
		ctx:         ctx,
		task:        appports.TurnTask{SessionID: task.SessionID, Run: replacementRunner(task.Run)},
		result:      make(chan error, 1),
		replacement: &task,
	}
	position := len(queue.pending)
	if queue.running {
		position++
	}
	queue.pending = append(queue.pending, queued)
	queue.replacement = queued
	select {
	case queue.wakeCh <- struct{}{}:
	default:
	}
	return queued.result, position, nil
}

func replacementRunner(run func(context.Context) error) func(context.Context) error {
	return func(ctx context.Context) (err error) {
		defer func() {
			if recover() != nil {
				err = ErrTurnReplacementFailed
			}
		}()
		return boundedReplacementError(run(ctx))
	}
}

func boundedReplacementError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, ErrTurnReplacementPending):
		return ErrTurnReplacementPending
	case errors.Is(err, ErrTurnReplacementConflict):
		return ErrTurnReplacementConflict
	case errors.Is(err, ErrTurnReplacementFailed):
		return ErrTurnReplacementFailed
	default:
		return ErrTurnReplacementFailed
	}
}

func completedTurnResult(err error) <-chan error {
	result := make(chan error, 1)
	result <- err
	close(result)
	return result
}

func queuePosition(queue *sessionTurnQueue, target *queuedTurn) int {
	if queue.runningTurn == target {
		return 0
	}
	for i, pending := range queue.pending {
		if pending == target {
			if queue.running {
				return i + 1
			}
			return i
		}
	}
	return 0
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
		PendingTurns:    pendingUserTurns(queue.pending),
	}, true
}

func pendingUserTurns(pending []*queuedTurn) int {
	count := 0
	for _, turn := range pending {
		if turn != nil && turn.replacement == nil {
			count++
		}
	}
	return count
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
		for _, turn := range droppedTurns {
			d.finishReplacementLocked(queue, turn, context.Canceled)
		}
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
		for _, turn := range queue.pending {
			d.finishReplacementLocked(queue, turn, context.Canceled)
		}
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
		d.finishReplacementLocked(queue, turn, err)
		d.mu.Unlock()
		completeTurn(turn, err)

		if err != nil && !errors.Is(err, context.Canceled) {
			if turn.replacement != nil {
				d.logger.Error().
					Str("error_class", replacementErrorClass(err)).
					Str("session_id", sessionID).
					Msg("replacement task failed")
			} else {
				d.logger.Error().Err(err).Str("session_id", sessionID).Msg("turn task failed")
			}
		}
	}
}

func replacementErrorClass(err error) string {
	switch {
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	case errors.Is(err, ErrTurnReplacementConflict):
		return "replacement_conflict"
	case errors.Is(err, ErrTurnReplacementPending):
		return "replacement_pending"
	default:
		return "replacement_failed"
	}
}

func (d *TurnDispatcher) finishReplacementLocked(queue *sessionTurnQueue, turn *queuedTurn, err error) {
	if queue == nil || turn == nil || turn.replacement == nil || queue.replacement != turn {
		return
	}
	rememberReplacementResult(queue, turn.replacement.OperationID, replacementResult{
		inputHash: turn.replacement.InputHash,
		err:       boundedReplacementError(err),
	})
	queue.replacement = nil
}

// rememberReplacementResult bounds in-memory replay evidence to the most
// recent operations in one live lane. Durable replay beyond this window belongs
// to the runtime operation store.
func rememberReplacementResult(queue *sessionTurnQueue, operationID string, result replacementResult) {
	if queue == nil {
		return
	}
	if queue.terminalReplacements == nil {
		queue.terminalReplacements = make(map[string]replacementResult)
	}
	if _, exists := queue.terminalReplacements[operationID]; exists {
		queue.terminalReplacements[operationID] = result
		return
	}
	if len(queue.terminalOrder) == terminalReplacementLimit {
		oldest := queue.terminalOrder[0]
		delete(queue.terminalReplacements, oldest)
		copy(queue.terminalOrder, queue.terminalOrder[1:])
		queue.terminalOrder = queue.terminalOrder[:len(queue.terminalOrder)-1]
	}
	queue.terminalReplacements[operationID] = result
	queue.terminalOrder = append(queue.terminalOrder, operationID)
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
			if d.evictIdleSessionLocked(sessionID, queue) {
				d.mu.Unlock()
				return nil, nil, nil, false
			}
			d.mu.Unlock()
			continue
		}
	}
}

func (d *TurnDispatcher) evictIdleSessionLocked(sessionID string, queue *sessionTurnQueue) bool {
	if d.sessions[sessionID] != queue || queue.running || len(queue.pending) != 0 {
		return false
	}
	delete(d.sessions, sessionID)
	return true
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
