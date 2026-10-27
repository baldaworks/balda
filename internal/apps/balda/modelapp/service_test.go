package modelapp

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actors"
	"github.com/baldaworks/balda/internal/apps/balda/appports"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/rs/zerolog"
)

type modelChangerFunc func(context.Context, baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error)

func (f modelChangerFunc) ChangeModel(ctx context.Context, request baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
	return f(ctx, request)
}

func TestSubmitUsesExistingSessionFIFOWithoutSteeringPayload(t *testing.T) {
	dispatcher := actors.NewTurnDispatcher(zerolog.Nop())
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := dispatcher.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown() = %v", err)
		}
	})

	var mu sync.Mutex
	order := make([]string, 0, 3)
	appendOrder := func(value string) {
		mu.Lock()
		defer mu.Unlock()
		order = append(order, value)
	}
	started := make(chan struct{})
	release := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session",
		Run: func(context.Context) error {
			close(started)
			<-release
			appendOrder("active")
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started

	var gotRequest baldasession.ModelChangeRequest
	service := newService(dispatcher, modelChangerFunc(func(_ context.Context, request baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
		gotRequest = request
		appendOrder("model")
		return baldasession.ModelChangeResult{Status: baldasession.ModelChangeChangedLive, Generation: 2}, nil
	}))
	completion, position, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Model: "model-2"})
	if err != nil || position != 1 {
		t.Fatalf("Submit() position=%d err=%v", position, err)
	}
	later, position, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session",
		Run: func(context.Context) error {
			appendOrder("later")
			return nil
		},
	})
	if err != nil || position != 2 {
		t.Fatalf("Enqueue(later) position=%d err=%v", position, err)
	}

	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	gotCompletion := <-completion
	if gotCompletion.Err != nil || gotCompletion.Result.Status != baldasession.ModelChangeChangedLive {
		t.Fatalf("completion = %+v", gotCompletion)
	}
	if err := <-later; err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	gotOrder := append([]string(nil), order...)
	mu.Unlock()
	if !reflect.DeepEqual(gotOrder, []string{"active", "model", "later"}) {
		t.Fatalf("order = %v", gotOrder)
	}
	if gotRequest.OperationID != "operation" || gotRequest.SessionID != "session" || gotRequest.TargetModel != "model-2" || len(gotRequest.InputHash) != 64 {
		t.Fatalf("request = %+v", gotRequest)
	}
}

func TestSubmitQueueRejectionHasNoDownstreamCall(t *testing.T) {
	wantErr := errors.New("queue rejected")
	queue := rejectingQueue{err: wantErr}
	calls := 0
	service := newService(queue, modelChangerFunc(func(context.Context, baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
		calls++
		return baldasession.ModelChangeResult{}, nil
	}))

	completion, position, err := service.Submit(t.Context(), Request{OperationID: "operation", SessionID: "session", Model: "model-2"})
	if !errors.Is(err, ErrUnavailable) || completion != nil || position != 0 {
		t.Fatalf("Submit() = %v, %d, %v", completion, position, err)
	}
	if calls != 0 {
		t.Fatalf("downstream calls = %d", calls)
	}
}

func TestModelInputHashIsVersionedCanonicalAndNormalized(t *testing.T) {
	request := Request{OperationID: "operation", SessionID: "session", Model: "model-2"}
	const want = "b17c7d2323240779d8d2c0e999a1963ef48457c507559818d5186e228e8f2757"
	if got := modelInputHash(request); got != want {
		t.Fatalf("modelInputHash() = %q, want %q", got, want)
	}
	spaced := Request{OperationID: " operation ", SessionID: "\tsession\n", Model: " model-2 "}
	if got := modelInputHash(spaced); got != want {
		t.Fatalf("normalized modelInputHash() = %q, want %q", got, want)
	}
}

func TestModelInputHashSeparatesEveryPublicField(t *testing.T) {
	base := Request{OperationID: "operation", SessionID: "session", Model: "model-2"}
	hashes := map[string]struct{}{modelInputHash(base): {}}
	variants := []Request{
		{OperationID: "operation-2", SessionID: base.SessionID, Model: base.Model},
		{OperationID: base.OperationID, SessionID: "session-2", Model: base.Model},
		{OperationID: base.OperationID, SessionID: base.SessionID, Model: "model-3"},
		{OperationID: "operatio", SessionID: "nsession", Model: base.Model},
	}
	for _, variant := range variants {
		hash := modelInputHash(variant)
		if _, collision := hashes[hash]; collision {
			t.Fatalf("hash collision for %+v: %s", variant, hash)
		}
		hashes[hash] = struct{}{}
	}
}

func TestSubmitQueueFullHasZeroDownstreamWork(t *testing.T) {
	dispatcher := testDispatcher(t)
	started := make(chan struct{})
	release := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session",
		Run: func(context.Context) error {
			close(started)
			<-release
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	var calls atomic.Int32
	service := newService(dispatcher, modelChangerFunc(func(context.Context, baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
		calls.Add(1)
		return baldasession.ModelChangeResult{}, nil
	}))
	accepted := make([]<-chan Completion, 0, 20)
	for index := range 20 {
		completion, _, err := service.Submit(t.Context(), Request{
			OperationID: fmt.Sprintf("operation-%d", index), SessionID: "session", Model: "model-2",
		})
		if err != nil {
			t.Fatalf("Submit(%d) = %v", index, err)
		}
		accepted = append(accepted, completion)
	}
	completion, _, err := service.Submit(t.Context(), Request{OperationID: "overflow", SessionID: "session", Model: "model-2"})
	if !errors.Is(err, ErrConflict) || completion != nil || calls.Load() != 0 {
		t.Fatalf("overflow = %v, %v; downstream calls=%d", completion, err, calls.Load())
	}
	if _, dropped, err := dispatcher.CancelSession(baldasession.SessionLocator{SessionID: "session"}, true); err != nil || dropped != 20 {
		t.Fatalf("CancelSession() dropped=%d err=%v", dropped, err)
	}
	for _, result := range accepted {
		if completed := <-result; !errors.Is(completed.Err, context.Canceled) {
			t.Fatalf("canceled queued completion = %+v", completed)
		}
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
}

func TestSubmitCanceledWhileQueuedHasZeroDownstreamWork(t *testing.T) {
	dispatcher := testDispatcher(t)
	started := make(chan struct{})
	release := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session", Run: func(context.Context) error { close(started); <-release; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	var calls atomic.Int32
	service := newService(dispatcher, modelChangerFunc(func(context.Context, baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
		calls.Add(1)
		return baldasession.ModelChangeResult{}, nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	completion, _, err := service.Submit(ctx, Request{OperationID: "operation", SessionID: "session", Model: "model-2"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if completed := <-completion; !errors.Is(completed.Err, context.Canceled) {
		t.Fatalf("completion = %+v", completed)
	}
	if calls.Load() != 0 {
		t.Fatalf("downstream calls = %d", calls.Load())
	}
}

func TestSubmitDoesNotBlockOtherSessions(t *testing.T) {
	dispatcher := testDispatcher(t)
	started := make(chan struct{})
	release := make(chan struct{})
	active, _, err := dispatcher.Enqueue(t.Context(), appports.TurnTask{
		SessionID: "session-a", Run: func(context.Context) error { close(started); <-release; return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	<-started
	called := make(chan string, 2)
	service := newService(dispatcher, modelChangerFunc(func(_ context.Context, request baldasession.ModelChangeRequest) (baldasession.ModelChangeResult, error) {
		called <- request.SessionID
		return baldasession.ModelChangeResult{Status: baldasession.ModelChangeChangedLive}, nil
	}))
	blocked, _, err := service.Submit(t.Context(), Request{OperationID: "operation-a", SessionID: "session-a", Model: "model-2"})
	if err != nil {
		t.Fatal(err)
	}
	independent, _, err := service.Submit(t.Context(), Request{OperationID: "operation-b", SessionID: "session-b", Model: "model-2"})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case completed := <-independent:
		if completed.Err != nil {
			t.Fatal(completed.Err)
		}
	case <-time.After(time.Second):
		t.Fatal("other session was blocked")
	}
	if got := <-called; got != "session-b" {
		t.Fatalf("first model session = %q", got)
	}
	select {
	case completed := <-blocked:
		t.Fatalf("blocked model completed early: %+v", completed)
	default:
	}
	close(release)
	if err := <-active; err != nil {
		t.Fatal(err)
	}
	if completed := <-blocked; completed.Err != nil {
		t.Fatal(completed.Err)
	}
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

type rejectingQueue struct{ err error }

func (q rejectingQueue) Enqueue(context.Context, appports.TurnTask) (<-chan error, int, error) {
	return nil, 0, q.err
}

func (rejectingQueue) CancelSession(baldasession.SessionLocator, bool) (bool, int, error) {
	return false, 0, nil
}
