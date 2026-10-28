package lifetimeapp

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
)

type fakeFreshLifetimeManager struct {
	mu sync.Mutex

	requests     []baldasession.FreshLifetimeRequest
	plans        map[*baldasession.PreparedFreshLifetime]baldasession.FreshLifetimeRequest
	terminal     map[string]baldasession.FreshLifetimeResult
	prepare      baldasession.FreshLifetimeResult
	prepareErr   error
	publish      baldasession.FreshLifetimeResult
	publishErr   error
	abort        baldasession.FreshLifetimeResult
	abortErr     error
	publishCalls int
	abortCalls   int
	publishHook  func()
}

func newFakeFreshLifetimeManager() *fakeFreshLifetimeManager {
	return &fakeFreshLifetimeManager{
		plans:    make(map[*baldasession.PreparedFreshLifetime]baldasession.FreshLifetimeRequest),
		terminal: make(map[string]baldasession.FreshLifetimeResult),
		publish:  baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeReset, Generation: 2},
		abort:    baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeUnavailable},
	}
}

func (m *fakeFreshLifetimeManager) PrepareFreshLifetime(_ context.Context, request baldasession.FreshLifetimeRequest) (*baldasession.PreparedFreshLifetime, baldasession.FreshLifetimeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests = append(m.requests, request)
	if terminal, ok := m.terminal[request.OperationID]; ok {
		return nil, terminal, nil
	}
	if m.prepare.Status != "" || m.prepareErr != nil {
		return nil, m.prepare, m.prepareErr
	}
	plan := &baldasession.PreparedFreshLifetime{}
	m.plans[plan] = request
	return plan, baldasession.FreshLifetimeResult{}, nil
}

func (m *fakeFreshLifetimeManager) PublishFreshLifetime(_ context.Context, plan *baldasession.PreparedFreshLifetime) (baldasession.FreshLifetimeResult, error) {
	m.mu.Lock()
	m.publishCalls++
	request := m.plans[plan]
	result, err := m.publish, m.publishErr
	if result.Status != "" {
		m.terminal[request.OperationID] = result
	}
	hook := m.publishHook
	m.mu.Unlock()
	if hook != nil {
		hook()
	}
	return result, err
}

func (m *fakeFreshLifetimeManager) AbortFreshLifetime(_ context.Context, plan *baldasession.PreparedFreshLifetime) (baldasession.FreshLifetimeResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.abortCalls++
	request := m.plans[plan]
	if terminal, ok := m.terminal[request.OperationID]; ok {
		return terminal, nil
	}
	return m.abort, m.abortErr
}

func (m *fakeFreshLifetimeManager) snapshot() ([]baldasession.FreshLifetimeRequest, int, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]baldasession.FreshLifetimeRequest(nil), m.requests...), m.publishCalls, m.abortCalls
}

func TestSubmitPreparesBeforeExistingReplacementFIFO(t *testing.T) {
	dispatcher := testDispatcher(t)
	manager := newFakeFreshLifetimeManager()

	var mu sync.Mutex
	order := make([]string, 0, 3)
	appendOrder := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}
	manager.publishHook = func() { appendOrder("replacement") }

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error {
		close(activeStarted)
		<-releaseActive
		appendOrder("active")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted
	priorStarted := make(chan struct{})
	releasePrior := make(chan struct{})
	prior, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error {
		close(priorStarted)
		<-releasePrior
		appendOrder("prior")
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}

	service := newService(dispatcher, manager)
	completion, position, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Command: "reset"})
	if err != nil || position != 2 {
		t.Fatalf("Submit() position=%d err=%v", position, err)
	}
	requests, publishCalls, _ := manager.snapshot()
	if len(requests) != 1 || publishCalls != 0 {
		t.Fatalf("pre-boundary state: requests=%d publishes=%d", len(requests), publishCalls)
	}
	if _, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error { return nil }}); !errors.Is(err, appports.ErrTurnReplacementPending) {
		t.Fatalf("later Enqueue() error = %v, want replacement pending", err)
	}

	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	<-priorStarted
	_, publishCalls, _ = manager.snapshot()
	if publishCalls != 0 {
		t.Fatalf("publish before prior turn completed: %d", publishCalls)
	}
	close(releasePrior)
	if err := <-prior; err != nil {
		t.Fatal(err)
	}
	got := <-completion
	if got.Result.Status != StatusReset || got.Result.Generation != 2 {
		t.Fatalf("completion = %+v", got)
	}
	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if !reflect.DeepEqual(gotOrder, []string{"active", "prior", "replacement"}) {
		t.Fatalf("order = %v", gotOrder)
	}
}

func TestSubmitExactRedeliveryPublishesOnceAndCollisionAborts(t *testing.T) {
	dispatcher := testDispatcher(t)
	manager := newFakeFreshLifetimeManager()
	service := newService(dispatcher, manager)

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error {
		close(activeStarted)
		<-releaseActive
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted

	request := Request{OperationID: "operation", SessionID: "session", Command: "reset"}
	first, _, err := service.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, _, err := service.Submit(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	conflict, _, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Command: "new"})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-conflict; got.Result.Status != StatusConflict {
		t.Fatalf("collision completion = %+v", got)
	}

	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	for index, completion := range []<-chan Completion{first, duplicate} {
		if got := <-completion; got.Result.Status != StatusReset {
			t.Fatalf("completion %d = %+v", index, got)
		}
	}
	requests, publishes, aborts := manager.snapshot()
	if len(requests) != 3 || publishes != 1 || aborts != 2 {
		t.Fatalf("calls: prepare=%d publish=%d abort=%d", len(requests), publishes, aborts)
	}
	if requests[0].InputHash != requests[1].InputHash || requests[0].InputHash == requests[2].InputHash {
		t.Fatalf("input hashes do not encode canonical alias: %#v", requests)
	}
}

func TestSubmitConcurrentExactRedeliveryPublishesOnce(t *testing.T) {
	dispatcher := testDispatcher(t)
	manager := newFakeFreshLifetimeManager()
	service := newService(dispatcher, manager)

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error {
		close(activeStarted)
		<-releaseActive
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted

	const submissions = 16
	completions := make(chan (<-chan Completion), submissions)
	errs := make(chan error, submissions)
	var wg sync.WaitGroup
	for range submissions {
		wg.Add(1)
		go func() {
			defer wg.Done()
			completion, _, submitErr := service.Submit(t.Context(), Request{
				OperationID: "operation",
				SessionID:   "session",
				Command:     "reset",
			})
			if submitErr != nil {
				errs <- submitErr
				return
			}
			completions <- completion
		}()
	}
	wg.Wait()
	close(completions)
	close(errs)
	for err := range errs {
		t.Fatalf("Submit() error = %v", err)
	}
	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	for completion := range completions {
		if got := <-completion; got.Result.Status != StatusReset {
			t.Fatalf("completion = %+v", got)
		}
	}
	requests, publishes, aborts := manager.snapshot()
	if len(requests) != submissions || publishes != 1 || aborts != submissions-1 {
		t.Fatalf("calls: prepare=%d publish=%d abort=%d", len(requests), publishes, aborts)
	}
}

func TestSubmitQueueFullAbortsWithoutPublication(t *testing.T) {
	manager := newFakeFreshLifetimeManager()
	service := newService(rejectingReplacementQueue{err: appports.ErrTurnQueueFull}, manager)

	completion, position, err := service.Submit(t.Context(), Request{
		OperationID: "operation",
		SessionID:   "session",
		Command:     "reset",
	})
	if err != nil || position != 0 {
		t.Fatalf("Submit() position=%d err=%v", position, err)
	}
	if got := <-completion; got.Result.Status != StatusConflict {
		t.Fatalf("completion = %+v", got)
	}
	_, publishes, aborts := manager.snapshot()
	if publishes != 0 || aborts != 1 {
		t.Fatalf("publish=%d abort=%d", publishes, aborts)
	}
}

func TestSubmitCancellationAbortsPreparedCandidate(t *testing.T) {
	dispatcher := testDispatcher(t)
	manager := newFakeFreshLifetimeManager()
	service := newService(dispatcher, manager)

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session", Run: func(context.Context) error {
		close(activeStarted)
		<-releaseActive
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	<-activeStarted
	ctx, cancel := context.WithCancel(t.Context())
	completion, _, err := service.Submit(ctx, Request{OperationID: "operation", SessionID: "session", Command: "reset"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(releaseActive)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if got := <-completion; got.Result.Status != StatusInterrupted {
		t.Fatalf("completion = %+v", got)
	}
	_, publishes, aborts := manager.snapshot()
	if publishes != 0 || aborts != 1 {
		t.Fatalf("publish=%d abort=%d", publishes, aborts)
	}
}

func TestSubmitRecoveryEvidenceWinsOverAdmissionFailure(t *testing.T) {
	manager := newFakeFreshLifetimeManager()
	manager.abort = baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeRecoveryRequired}
	manager.abortErr = baldasession.ErrRuntimeRecoveryRequired
	service := newService(rejectingReplacementQueue{err: appports.ErrTurnQueueFull}, manager)

	completion, _, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Command: "reset"})
	if err != nil {
		t.Fatal(err)
	}
	if got := <-completion; got.Result.Status != StatusRecoveryRequired {
		t.Fatalf("completion = %+v", got)
	}
}

func TestSubmitDoesNotBlockOtherSessions(t *testing.T) {
	dispatcher := testDispatcher(t)
	manager := newFakeFreshLifetimeManager()
	service := newService(dispatcher, manager)

	activeStarted := make(chan struct{})
	releaseActive := make(chan struct{})
	if _, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{SessionID: "session-a", Run: func(context.Context) error {
		close(activeStarted)
		<-releaseActive
		return nil
	}}); err != nil {
		t.Fatal(err)
	}
	<-activeStarted
	blocked, _, err := service.Submit(t.Context(), Request{OperationID: "operation-a", SessionID: "session-a", Command: "reset"})
	if err != nil {
		t.Fatal(err)
	}
	independent, _, err := service.Submit(t.Context(), Request{OperationID: "operation-b", SessionID: "session-b", Command: "reset"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-independent:
		if got.Result.Status != StatusReset {
			t.Fatalf("independent completion = %+v", got)
		}
	case <-t.Context().Done():
		t.Fatal(t.Context().Err())
	}
	select {
	case got := <-blocked:
		t.Fatalf("blocked session completed early: %+v", got)
	default:
	}
	close(releaseActive)
	if got := <-blocked; got.Result.Status != StatusReset {
		t.Fatalf("blocked completion = %+v", got)
	}
}

func TestLifetimeInputHashIsVersionedCanonicalAndSeparated(t *testing.T) {
	request := Request{OperationID: "operation", SessionID: "session", Command: "reset"}
	const want = "c0b49e3a7b97596b67688023bc941fdd43082fb964c3933c9cd6cdb4e6ffa88e"
	if got := lifetimeInputHash(request); got != want {
		t.Fatalf("lifetimeInputHash() = %q, want %q", got, want)
	}
	if got := lifetimeInputHash(Request{OperationID: " operation ", SessionID: "\tsession\n", Command: " RESET "}); got != want {
		t.Fatalf("normalized hash = %q, want %q", got, want)
	}
	hashes := map[string]struct{}{want: {}}
	for _, variant := range []Request{
		{OperationID: "operation-2", SessionID: "session", Command: "reset"},
		{OperationID: "operation", SessionID: "session-2", Command: "reset"},
		{OperationID: "operation", SessionID: "session", Command: "new"},
		{OperationID: "operatio", SessionID: "nsession", Command: "reset"},
	} {
		hash := lifetimeInputHash(variant)
		if _, exists := hashes[hash]; exists {
			t.Fatalf("hash collision for %+v: %s", variant, hash)
		}
		hashes[hash] = struct{}{}
	}
}

func TestSubmitMapsBoundedResults(t *testing.T) {
	tests := []struct {
		name   string
		result baldasession.FreshLifetimeResult
		err    error
		want   Status
	}{
		{name: "reset", result: baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeReset}, want: StatusReset},
		{name: "conflict", result: baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeConflict}, want: StatusConflict},
		{name: "unavailable", result: baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeUnavailable}, want: StatusUnavailable},
		{name: "interrupted", result: baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeUnavailable}, err: context.Canceled, want: StatusInterrupted},
		{name: "recovery", result: baldasession.FreshLifetimeResult{Status: baldasession.FreshLifetimeRecoveryRequired}, err: baldasession.ErrRuntimeRecoveryRequired, want: StatusRecoveryRequired},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manager := newFakeFreshLifetimeManager()
			manager.prepare, manager.prepareErr = test.result, test.err
			service := newService(rejectingReplacementQueue{}, manager)
			completion, _, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Command: "reset"})
			if err != nil {
				t.Fatal(err)
			}
			if got := <-completion; got.Result.Status != test.want {
				t.Fatalf("completion = %+v, want %s", got, test.want)
			}
		})
	}
}

func TestSubmitRejectsInvalidRequestWithoutPreparation(t *testing.T) {
	manager := newFakeFreshLifetimeManager()
	service := newService(rejectingReplacementQueue{}, manager)
	for index, request := range []Request{
		{},
		{OperationID: "operation", SessionID: "session"},
		{OperationID: "operation", SessionID: "session", Command: "provider"},
	} {
		completion, position, err := service.Submit(t.Context(), request)
		if !errors.Is(err, ErrInvalidRequest) || completion != nil || position != 0 {
			t.Fatalf("Submit(%d) = %v, %d, %v", index, completion, position, err)
		}
	}
	requests, _, _ := manager.snapshot()
	if len(requests) != 0 {
		t.Fatalf("preparations = %d", len(requests))
	}
}

type rejectingReplacementQueue struct {
	err error
}

func (q rejectingReplacementQueue) EnqueueReplacement(context.Context, appports.ReplacementTask) (<-chan error, int, error) {
	return nil, 0, q.err
}

func testDispatcher(t *testing.T) *actors.TurnDispatcher {
	t.Helper()
	dispatcher := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})
	return dispatcher
}
