package actors

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	baldaactorcmd "github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

func TestNewSessionActorWiresRuntimeStateUpdater(t *testing.T) {
	t.Parallel()

	manager := &baldasession.Manager{}
	actor, ok := newSessionActor(sessionActorExecutorParams{Sessions: manager}).(*sessionActorExecutor)
	if !ok {
		t.Fatalf("newSessionActor() type = %T", actor)
	}
	if actor.sessions != manager {
		t.Fatal("newSessionActor() did not wire session runtime state updater")
	}
}

func TestSessionActorInterruptQueueModeCancelsSessionBeforeEnqueue(t *testing.T) {
	t.Parallel()

	turns := &fakeTurnDispatcher{}
	exec := &sessionActorExecutor{
		turns:  turns,
		runner: fakeSessionTurnRunner{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := exec.enqueueTurn(ctx, testSessionTurnEnvelope(t, map[string]string{"queue_mode": baldaexecution.QueueModeInterrupt}))
	if err == nil {
		t.Fatal("enqueueTurn() error = nil, want canceled context after enqueue")
	}
	if len(turns.cancelCalls) != 1 {
		t.Fatalf("CancelSession calls = %d, want 1", len(turns.cancelCalls))
	}
	if got := turns.cancelCalls[0]; got.SessionID != "tg-9001-77" || !got.ClearQueued {
		t.Fatalf("CancelSession call = %+v, want session=tg-9001-77 clearQueued=true", got)
	}
	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
}

func TestSessionActorDefaultQueueModeDoesNotCancelSession(t *testing.T) {
	t.Parallel()

	turns := &fakeTurnDispatcher{}
	exec := &sessionActorExecutor{
		turns:  turns,
		runner: fakeSessionTurnRunner{},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := exec.enqueueTurn(ctx, testSessionTurnEnvelope(t, nil))
	if err == nil {
		t.Fatal("enqueueTurn() error = nil, want canceled context after enqueue")
	}
	if len(turns.cancelCalls) != 0 {
		t.Fatalf("CancelSession calls = %d, want 0", len(turns.cancelCalls))
	}
	if len(turns.enqueueCalls) != 1 {
		t.Fatalf("Enqueue calls = %d, want 1", len(turns.enqueueCalls))
	}
}

func TestSessionActorRejectsMismatchedEnvelopeAndPayloadJobID(t *testing.T) {
	t.Parallel()

	exec := &sessionActorExecutor{}
	env := testSessionTurnEnvelopeWithJobID(t, nil, "task-payload", sessionTurnSourceWebhook)
	env.Namespace = baldaexecution.NamespaceWebhookInbound
	env.Meta = baldaexecution.WithJobIDMeta(nil, "task-envelope")

	err := exec.enqueueTurn(context.Background(), env)
	if err == nil {
		t.Fatal("enqueueTurn() error = nil, want policy error")
	}
	if got, want := actorlayer.ClassifyError(err), actorlayer.ErrorKindPolicy; got != want {
		t.Fatalf("enqueueTurn() error kind = %q, want %q (err=%v)", got, want, err)
	}
}

func TestSessionActorSettleSessionTurnResultMarksTaskFailedWithoutRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	created, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-session-failed",
		SessionID: "tg-9001-77",
		Objective: "run session task",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created {
		t.Fatal("Create() created = false, want true")
	}

	exec := &sessionActorExecutor{tasks: tasks}
	runErr := errors.New("runner failed")
	env := testSessionTurnEnvelopeWithJobID(t, nil, "task-session-failed", sessionTurnSourceWebhook)
	env.Namespace = baldaexecution.NamespaceWebhookInbound
	payload := SessionTurnPayload{JobID: "task-session-failed", Source: sessionTurnSourceWebhook}
	settlement := newSessionSettlementCoordinator(exec.tasks, exec.scheduler)

	if err := settlement.settle(ctx, env, payload, runErr); err != nil {
		t.Fatalf("settle() error = %v, want nil after recording task failure", err)
	}

	task, ok, err := tasks.Get(ctx, baldaexecution.EnvelopeJobID(env))
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatalf("Get() found = false for task %q", baldaexecution.EnvelopeJobID(env))
	}
	if task.Status != baldastate.JobStatusFailed {
		t.Fatalf("task status = %q, want %q", task.Status, baldastate.JobStatusFailed)
	}
}

func TestSessionActorSettleSessionTurnResultMarksTaskCanceledWithoutRetry(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	created, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-session-canceled",
		SessionID: "tg-9001-77",
		Objective: "run session task",
		Status:    baldastate.JobStatusRunning,
	}, "test", nil)
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if !created {
		t.Fatal("Create() created = false, want true")
	}

	exec := &sessionActorExecutor{tasks: tasks}
	env := testSessionTurnEnvelopeWithJobID(t, nil, "task-session-canceled", sessionTurnSourceWebhook)
	env.Namespace = baldaexecution.NamespaceWebhookInbound
	payload := SessionTurnPayload{JobID: "task-session-canceled", Source: sessionTurnSourceWebhook}
	settlement := newSessionSettlementCoordinator(exec.tasks, exec.scheduler)

	if err := settlement.settle(ctx, env, payload, context.Canceled); err != nil {
		t.Fatalf("settle() error = %v, want nil after recording task cancellation", err)
	}

	task, ok, err := tasks.Get(ctx, baldaexecution.EnvelopeJobID(env))
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatalf("Get() found = false for task %q", baldaexecution.EnvelopeJobID(env))
	}
	if task.Status != baldastate.JobStatusCanceled {
		t.Fatalf("task status = %q, want %q", task.Status, baldastate.JobStatusCanceled)
	}
}

func TestSessionActorSettleSessionTurnResultKeepsNonTaskErrorsRetryable(t *testing.T) {
	t.Parallel()

	settlement := newSessionSettlementCoordinator(nil, nil)
	runErr := errors.New("runner failed")

	err := settlement.settle(context.Background(), testSessionTurnEnvelope(t, nil), SessionTurnPayload{}, runErr)
	if !errors.Is(err, runErr) {
		t.Fatalf("settle() error = %v, want original run error", err)
	}
}

func TestSessionActorSettleSessionTurnResultKeepsHumanTurnErrorsRetryableEvenWithJobID(t *testing.T) {
	t.Parallel()

	settlement := newSessionSettlementCoordinator(nil, nil)
	env := testSessionTurnEnvelopeWithJobID(t, nil, "turn-legacy-1", sessionTurnSourceTelegram)
	runErr := errors.New("runner failed")

	err := settlement.settle(context.Background(), env, SessionTurnPayload{Source: sessionTurnSourceTelegram}, runErr)
	if !errors.Is(err, runErr) {
		t.Fatalf("settle() error = %v, want original run error", err)
	}
}

func TestSessionActorSettleSessionTurnResultRecordsScheduledJobOutcome(t *testing.T) {
	t.Parallel()

	recorder := &fakeScheduledJobRecorder{}
	exec := &sessionActorExecutor{scheduler: recorder}
	payload := SessionTurnPayload{JobID: "runtime-task-1", ScheduledJobID: "scheduled-1"}
	env := testSessionTurnEnvelopeWithJobID(t, nil, "runtime-task-1", sessionTurnSourceSchedule)
	settlement := newSessionSettlementCoordinator(exec.tasks, exec.scheduler)

	if err := settlement.settle(context.Background(), env, payload, nil); err != nil {
		t.Fatalf("settle(success) error = %v", err)
	}
	if len(recorder.successes) != 1 || recorder.successes[0] != "scheduled-1" {
		t.Fatalf("successes = %#v, want [scheduled-1]", recorder.successes)
	}
	if len(recorder.failures) != 0 {
		t.Fatalf("failures = %d, want 0", len(recorder.failures))
	}

	runErr := errors.New("scheduled run failed")
	if err := settlement.settle(context.Background(), env, payload, runErr); err != nil {
		t.Fatalf("settle(failure) error = %v, want nil after recording scheduled job failure", err)
	}
	if len(recorder.failures) != 1 {
		t.Fatalf("failures = %d, want 1", len(recorder.failures))
	}
	if got := recorder.failures[0]; got.jobID != "scheduled-1" || !errors.Is(got.cause, runErr) {
		t.Fatalf("failure = %+v, want task scheduled-1 with original error", got)
	}
}

func TestSessionActorHandlesScheduledQuestionTimeout(t *testing.T) {
	t.Parallel()

	store := &fakeQuestionStoreForTimeout{
		record: baldastate.QuestionRecord{
			QuestionID:      "question-1",
			Status:          questioncmd.StatusPending,
			InteractionJSON: `{"session_id":"tg-1-0","channel_kind":"telegram","locator":{"session_id":"tg-1-0","channel_type":"telegram","address_key":"1:0","address_json":"{\"chat_id\":1}"}}`,
			ResumeJSON:      `{"to":"goalkeeper:goal-1","namespace":"goalkeeper.command"}`,
		},
	}
	dispatcher := &fakeTurnDispatcher{}
	exec := &sessionActorExecutor{
		dispatcher: dispatcher,
		questions:  questions.New(store, nil, zerolog.Nop()),
		scheduler:  &fakeScheduledJobRecorder{},
	}
	content, err := questioncmd.TimeoutScheduledContent("question-1")
	if err != nil {
		t.Fatalf("TimeoutScheduledContent() error = %v", err)
	}
	env := testSessionTurnEnvelopeWithJobID(t, nil, "scheduled-job-1", sessionTurnSourceSchedule)
	env.Namespace = baldaexecution.NamespaceScheduleInbound
	payload := SessionTurnPayload{
		JobID:          "scheduled-job-1",
		ScheduledJobID: "question-timeout-question-1",
		Source:         sessionTurnSourceSchedule,
		Text:           content,
		Locator:        baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1}`},
	}
	env.Payload, err = actorlayer.MarshalPayload(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if err := exec.enqueueTurn(context.Background(), env); err != nil {
		t.Fatalf("enqueueTurn() error = %v", err)
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
	if dispatcher.commands[0].Kind != baldaactorcmd.KindQuestionTimedOut {
		t.Fatalf("kind = %q, want %q", dispatcher.commands[0].Kind, baldaactorcmd.KindQuestionTimedOut)
	}
}

type fakeQuestionStoreForTimeout struct {
	record baldastate.QuestionRecord
}

func (*fakeQuestionStoreForTimeout) CreatePendingQuestion(context.Context, baldastate.QuestionRecord) error {
	return nil
}
func (*fakeQuestionStoreForTimeout) BindQuestionDeliveryRef(context.Context, string, questioncmd.DeliveryRef) error {
	return nil
}
func (f *fakeQuestionStoreForTimeout) GetQuestionByID(_ context.Context, questionID string) (baldastate.QuestionRecord, bool, error) {
	return f.record, f.record.QuestionID == questionID, nil
}
func (*fakeQuestionStoreForTimeout) GetPendingQuestionByReplyRef(context.Context, string, string, string) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}
func (*fakeQuestionStoreForTimeout) MarkQuestionAnswered(context.Context, string, questioncmd.Answer) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}
func (f *fakeQuestionStoreForTimeout) MarkQuestionTimedOut(_ context.Context, questionID string, timedOutAt time.Time) (baldastate.QuestionRecord, bool, error) {
	if f.record.QuestionID != questionID {
		return baldastate.QuestionRecord{}, false, nil
	}
	f.record.Status = questioncmd.StatusTimedOut
	f.record.AnsweredAt = timedOutAt
	return f.record, true, nil
}

func TestSessionActorEnqueueTurnSkipsDeadLetteredTask(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	provider, bus, dispatcher, tasks, allocator := newTaskActorRuntimeServices(t, ctx)
	_ = provider
	_ = bus
	_ = dispatcher
	_ = allocator
	if _, err := tasks.Create(ctx, baldastate.JobRecord{
		ID:        "task-session-deadlettered",
		SessionID: "tg-9001-77",
		Objective: "run session task",
		Status:    baldastate.JobStatusDeadLettered,
	}, "test", nil); err != nil {
		t.Fatalf("Create() error = %v", err)
	}

	turns := &fakeTurnDispatcher{}
	exec := &sessionActorExecutor{
		turns:  turns,
		runner: fakeSessionTurnRunner{},
		tasks:  tasks,
	}
	env := testSessionTurnEnvelopeWithJobID(t, nil, "task-session-deadlettered", sessionTurnSourceWebhook)
	env.Namespace = baldaexecution.NamespaceWebhookInbound

	if err := exec.enqueueTurn(ctx, env); err != nil {
		t.Fatalf("enqueueTurn() error = %v, want nil noop for deadlettered task", err)
	}
	if len(turns.enqueueCalls) != 0 {
		t.Fatalf("Enqueue calls = %d, want 0 for deadlettered task", len(turns.enqueueCalls))
	}
}

func testSessionTurnEnvelope(t *testing.T, meta map[string]string) actorlayer.Envelope {
	t.Helper()

	locator := baldasession.SessionLocator{
		ChannelType: "telegram",
		AddressKey:  "tg-9001-77",
		AddressJSON: `{"chat_id":9001,"topic_id":77}`,
		SessionID:   "tg-9001-77",
	}
	payload, err := json.Marshal(SessionTurnPayload{
		Text:    "run this",
		Locator: locator,
		Deliver: false,
		Source:  sessionTurnSourceTelegram,
	})
	if err != nil {
		t.Fatalf("Marshal(SessionTurnPayload) error = %v", err)
	}
	return actorlayer.Envelope{
		ID:        "session-command-1",
		Namespace: baldaexecution.NamespaceHumanInbound,
		Kind:      baldaexecution.KindMessage,
		From:      actorlayer.ActorAddress{Target: "telegram", Key: "101"},
		To:        actorlayer.ActorAddress{Target: baldaexecution.ActorTypeSession, Key: locator.SessionID},
		Payload: actorlayer.Payload{
			Encoding: actorlayer.EncodingJSON,
			Data:     payload,
		},
		Meta: baldaexecution.WithSessionIDMeta(meta, locator.SessionID),
	}
}

func testSessionTurnEnvelopeWithJobID(t *testing.T, meta map[string]string, jobID string, source string) actorlayer.Envelope {
	t.Helper()
	env := testSessionTurnEnvelope(t, meta)
	var payload SessionTurnPayload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(SessionTurnPayload) error = %v", err)
	}
	payload.JobID = jobID
	env.Meta = baldaexecution.WithJobIDMeta(env.Meta, jobID)
	if strings.TrimSpace(source) != "" {
		payload.Source = source
	}
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(SessionTurnPayload with JobID) error = %v", err)
	}
	env.Payload = actorlayer.Payload{
		Encoding: actorlayer.EncodingJSON,
		Data:     data,
	}
	return env
}

type fakeSessionTurnRunner struct{}

func (fakeSessionTurnRunner) RunSessionTurnPayload(context.Context, SessionTurnPayload) error {
	return nil
}

type fakeScheduledJobRecorder struct {
	successes []string
	failures  []scheduledJobFailure
}

type scheduledJobFailure struct {
	jobID string
	cause error
}

func (f *fakeScheduledJobRecorder) MarkSuccess(_ context.Context, jobID string) error {
	f.successes = append(f.successes, jobID)
	return nil
}

func (f *fakeScheduledJobRecorder) RecordExecutionFailure(_ context.Context, jobID string, cause error) error {
	f.failures = append(f.failures, scheduledJobFailure{jobID: jobID, cause: cause})
	return nil
}

type callbackSessionTurnRunner struct {
	runFn func(context.Context, SessionTurnPayload) error
}

func (c callbackSessionTurnRunner) RunSessionTurnPayload(ctx context.Context, payload SessionTurnPayload) error {
	if c.runFn != nil {
		return c.runFn(ctx, payload)
	}
	return nil
}

func testSessionSteeringEnvelope(t *testing.T, sessionID string, messageID int, replyToMessageID int, dedupeKey string, text string) actorlayer.Envelope {
	t.Helper()
	payload := SessionTurnPayload{
		Locator: baldasession.SessionLocator{
			ChannelType: "telegram",
			AddressKey:  sessionID,
			AddressJSON: `{"chat_id":9001,"topic_id":77}`,
			SessionID:   sessionID,
		},
		RequesterUserID:  testTelegramUserID101,
		MessageID:        messageID,
		ReplyToMessageID: replyToMessageID,
		DedupeKey:        dedupeKey,
		Text:             text,
		Source:           sessionTurnSourceTelegram,
	}
	env, err := SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatalf("SessionTurnEnvelope() error = %v", err)
	}
	return env
}

func ensureNoErrorSignal(t *testing.T, ch <-chan error, wait time.Duration, label string) {
	t.Helper()
	select {
	case err := <-ch:
		t.Fatalf("unexpected error signal (%v): %s", err, label)
	case <-time.After(wait):
	}
}

func TestSessionActor_SteeringEnvelopesDoNotAcknowledgeBeforeExecutionBegins(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	rootStarted := make(chan struct{})
	rootRelease := make(chan struct{})
	batchStarted := make(chan struct{})
	batchRelease := make(chan struct{})

	runner := callbackSessionTurnRunner{
		runFn: func(_ context.Context, p SessionTurnPayload) error {
			if len(p.SteeringMessages) == 0 {
				close(rootStarted)
				<-rootRelease
				return nil
			}
			close(batchStarted)
			<-batchRelease
			return nil
		},
	}
	exec := &sessionActorExecutor{
		turns:  dispatcher,
		runner: runner,
	}

	sessionID := "tg-steer-ack-1"
	rootEnv := testSessionSteeringEnvelope(t, sessionID, 100, 0, "root-100", "root message")
	steer1Env := testSessionSteeringEnvelope(t, sessionID, 101, 100, "steer-101", "first correction")
	steer2Env := testSessionSteeringEnvelope(t, sessionID, 102, 101, "steer-102", "second correction")

	rootDone := make(chan error, 1)
	go func() {
		rootDone <- exec.enqueueTurn(context.Background(), rootEnv)
	}()
	waitForSignal(t, rootStarted, "root start")

	steer1Done := make(chan error, 1)
	go func() {
		steer1Done <- exec.enqueueTurn(context.Background(), steer1Env)
	}()
	time.Sleep(30 * time.Millisecond)

	steer2Done := make(chan error, 1)
	go func() {
		steer2Done <- exec.enqueueTurn(context.Background(), steer2Env)
	}()

	// Assert that neither steering constituent acknowledges while root is executing.
	ensureNoErrorSignal(t, steer1Done, 200*time.Millisecond, "steer1 must not acknowledge while root is running")
	ensureNoErrorSignal(t, steer2Done, 200*time.Millisecond, "steer2 must not acknowledge while root is running")

	// Release root execution and verify it settles.
	close(rootRelease)
	select {
	case err := <-rootDone:
		if err != nil {
			t.Fatalf("rootDone error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for rootDone")
	}

	// Wait for batch execution to start.
	waitForSignal(t, batchStarted, "batch execution start")

	// Even while batch is running, steering constituents must not prematurely acknowledge.
	ensureNoErrorSignal(t, steer1Done, 200*time.Millisecond, "steer1 must not acknowledge while batch is running")
	ensureNoErrorSignal(t, steer2Done, 200*time.Millisecond, "steer2 must not acknowledge while batch is running")

	// Release batch execution.
	close(batchRelease)

	// Now both steering constituents must complete successfully (ACK).
	select {
	case err := <-steer1Done:
		if err != nil {
			t.Fatalf("steer1Done error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for steer1Done")
	}

	select {
	case err := <-steer2Done:
		if err != nil {
			t.Fatalf("steer2Done error = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for steer2Done")
	}
}

func TestSessionActor_SimulatedRestartWithUnacknowledgedEnvelopesReplaysAllSteering(t *testing.T) {
	t.Parallel()

	dispatcher1 := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}

	rootStarted := make(chan struct{})
	blockRoot := make(chan struct{})

	runner1 := callbackSessionTurnRunner{
		runFn: func(_ context.Context, _ SessionTurnPayload) error {
			close(rootStarted)
			<-blockRoot
			return nil
		},
	}
	exec1 := &sessionActorExecutor{
		turns:  dispatcher1,
		runner: runner1,
	}

	sessionID := "tg-steer-restart-1"
	rootEnv := testSessionSteeringEnvelope(t, sessionID, 200, 0, "root-200", "original prompt")
	steer1Env := testSessionSteeringEnvelope(t, sessionID, 201, 200, "steer-201", "correction 1")
	steer2Env := testSessionSteeringEnvelope(t, sessionID, 202, 201, "steer-202", "correction 2")

	go func() {
		_ = exec1.enqueueTurn(context.Background(), rootEnv)
	}()
	waitForSignal(t, rootStarted, "root start on instance 1")

	steer1Done := make(chan error, 1)
	go func() {
		steer1Done <- exec1.enqueueTurn(context.Background(), steer1Env)
	}()
	steer2Done := make(chan error, 1)
	go func() {
		steer2Done <- exec1.enqueueTurn(context.Background(), steer2Env)
	}()

	ensureNoErrorSignal(t, steer1Done, 150*time.Millisecond, "steer1 unacknowledged")
	ensureNoErrorSignal(t, steer2Done, 150*time.Millisecond, "steer2 unacknowledged")

	// Simulate crash: shut down dispatcher1 and abandon instance 1.
	close(blockRoot)
	_ = dispatcher1.Shutdown(context.Background())

	// Reconstruct fresh runtime instance (instance 2).
	dispatcher2 := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher2.Shutdown(context.Background()) }()

	var mu sync.Mutex
	var executedBatches []SessionTurnPayload
	replayedRootStarted := make(chan struct{})
	replayedRootRelease := make(chan struct{})
	replayedDone := make(chan struct{})

	runner2 := callbackSessionTurnRunner{
		runFn: func(_ context.Context, p SessionTurnPayload) error {
			mu.Lock()
			executedBatches = append(executedBatches, p)
			count := len(executedBatches)
			mu.Unlock()

			if count == 1 {
				close(replayedRootStarted)
				<-replayedRootRelease
				return nil
			}
			if count == 2 {
				close(replayedDone)
			}
			return nil
		},
	}
	exec2 := &sessionActorExecutor{
		turns:  dispatcher2,
		runner: runner2,
	}

	// Redeliver original unacknowledged root envelope.
	replayedRootDone := make(chan error, 1)
	go func() {
		replayedRootDone <- exec2.enqueueTurn(context.Background(), rootEnv)
	}()
	waitForSignal(t, replayedRootStarted, "replayed root start on instance 2")

	// While root is executing, redeliver steering envelopes in sequence plus simulate a duplicate delivery of steer1Env.
	replayedSteer1Done := make(chan error, 1)
	go func() {
		replayedSteer1Done <- exec2.enqueueTurn(context.Background(), steer1Env)
	}()
	time.Sleep(30 * time.Millisecond)

	replayedSteer1DupDone := make(chan error, 1)
	go func() {
		replayedSteer1DupDone <- exec2.enqueueTurn(context.Background(), steer1Env)
	}()
	time.Sleep(30 * time.Millisecond)

	replayedSteer2Done := make(chan error, 1)
	go func() {
		replayedSteer2Done <- exec2.enqueueTurn(context.Background(), steer2Env)
	}()
	time.Sleep(30 * time.Millisecond)

	// Release root execution to let the merged steering batch run.
	close(replayedRootRelease)
	waitForSignal(t, replayedDone, "replayed execution of all turns")

	// All redelivered envelopes must settle successfully.
	for idx, ch := range []<-chan error{replayedRootDone, replayedSteer1Done, replayedSteer1DupDone, replayedSteer2Done} {
		select {
		case err := <-ch:
			if err != nil {
				t.Fatalf("replayed result[%d] error = %v, want nil", idx, err)
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for replayed result[%d]", idx)
		}
	}

	mu.Lock()
	defer mu.Unlock()
	if len(executedBatches) != 2 {
		t.Fatalf("executed batches = %d, want 2 (root, then merged steering)", len(executedBatches))
	}

	// First batch is root.
	if executedBatches[0].Text != "original prompt" {
		t.Fatalf("batch[0] text = %q, want original prompt", executedBatches[0].Text)
	}

	// Second batch is merged steering, containing both corrections and no duplicates.
	steeringBatch := executedBatches[1]
	if len(steeringBatch.SteeringMessages) != 2 {
		t.Fatalf("replayed steering messages len = %d, want 2", len(steeringBatch.SteeringMessages))
	}
	if steeringBatch.SteeringMessages[0].Text != "correction 1" || steeringBatch.SteeringMessages[1].Text != "correction 2" {
		t.Fatalf("replayed steering messages texts = %+v", steeringBatch.SteeringMessages)
	}
}

func TestSessionActor_TransportContextInterruptionKeepsEnvelopeRetryable(t *testing.T) {
	t.Parallel()

	dispatcher := &TurnDispatcher{
		logger:   zerolog.Nop(),
		sessions: make(map[string]*sessionTurnQueue),
		stopCh:   make(chan struct{}),
	}
	defer func() { _ = dispatcher.Shutdown(context.Background()) }()

	rootStarted := make(chan struct{})
	rootRelease := make(chan struct{})

	runner := callbackSessionTurnRunner{
		runFn: func(_ context.Context, p SessionTurnPayload) error {
			if len(p.SteeringMessages) == 0 {
				close(rootStarted)
				<-rootRelease
			}
			return nil
		},
	}
	exec := &sessionActorExecutor{
		turns:  dispatcher,
		runner: runner,
	}

	sessionID := "tg-steer-ctx-1"
	rootEnv := testSessionSteeringEnvelope(t, sessionID, 300, 0, "root-300", "root message")
	steerEnv := testSessionSteeringEnvelope(t, sessionID, 301, 300, "steer-301", "correction")

	go func() {
		_ = exec.enqueueTurn(context.Background(), rootEnv)
	}()
	waitForSignal(t, rootStarted, "root start")

	transportCtx, cancelTransport := context.WithCancel(context.Background())
	steerDone := make(chan error, 1)
	go func() {
		steerDone <- exec.enqueueTurn(transportCtx, steerEnv)
	}()

	// Wait briefly to ensure steerEnv is enqueued in the dispatcher.
	time.Sleep(50 * time.Millisecond)

	// Interrupt transport delivery context while steering is still waiting for batch execution.
	cancelTransport()

	select {
	case err := <-steerDone:
		if err == nil {
			t.Fatal("steerDone error = nil, want transient error on transport context cancellation")
		}
		if kind := actorlayer.ClassifyError(err); kind != actorlayer.ErrorKindTransient {
			t.Fatalf("steerDone error kind = %q, want %q (err=%v)", kind, actorlayer.ErrorKindTransient, err)
		}
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("steerDone err = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for steerDone to abort")
	}

	close(rootRelease)
}
