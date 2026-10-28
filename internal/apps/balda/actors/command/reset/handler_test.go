package reset

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/lifetimeapp"
	"github.com/baldaworks/balda/internal/apps/balda/runtimecatalogcmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

const resetTestSessionID = "tg-1-0"

type lifetimeSubmitter struct {
	mu       sync.Mutex
	requests []lifetimeapp.Request
	called   chan struct{}
	callOnce sync.Once
	result   lifetimeapp.Completion
	resultCh <-chan lifetimeapp.Completion
	nilCh    bool
	err      error
}

func (s *lifetimeSubmitter) Submit(_ context.Context, request lifetimeapp.Request) (<-chan lifetimeapp.Completion, int, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	result, resultCh, nilCh, err := s.result, s.resultCh, s.nilCh, s.err
	s.mu.Unlock()
	if s.called != nil {
		s.callOnce.Do(func() { close(s.called) })
	}
	if err != nil {
		return nil, 0, err
	}
	if nilCh {
		return nil, 1, nil
	}
	if resultCh != nil {
		return resultCh, 1, nil
	}
	completion := make(chan lifetimeapp.Completion, 1)
	completion <- result
	close(completion)
	return completion, 1, nil
}

func (s *lifetimeSubmitter) snapshot() []lifetimeapp.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]lifetimeapp.Request(nil), s.requests...)
}

type responseDispatcher struct {
	mu        sync.Mutex
	envelopes []actorlayer.Envelope
	contexts  []context.Context
	block     <-chan struct{}
	err       error
}

func (d *responseDispatcher) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if d.block != nil {
		select {
		case <-d.block:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if d.err != nil {
		return nil, d.err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.envelopes = append(d.envelopes, env)
	d.contexts = append(d.contexts, ctx)
	return &actortransport.DispatchReceipt{}, nil
}

func (d *responseDispatcher) last(t *testing.T) (actorlayer.Envelope, deliverycmd.Payload) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.envelopes) == 0 {
		t.Fatal("no response delivered")
	}
	env := d.envelopes[len(d.envelopes)-1]
	var payload deliverycmd.Payload
	if err := json.Unmarshal(env.Payload.Data, &payload); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return env, payload
}

func TestHandleAuthorizesBeforeLifetimeWork(t *testing.T) {
	for _, test := range []struct {
		name   string
		access commandcmd.Access
		want   bool
	}{
		{name: "denied"},
		{name: "session commands", access: commandcmd.Access{SessionCommands: true}, want: true},
		{name: "owner", access: commandcmd.Access{Owner: true}, want: true},
		{name: "collaborator", access: commandcmd.Access{Collaborator: true}, want: true},
		{name: "workspace member", access: commandcmd.Access{WorkspaceMember: true}, want: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifetimes := &lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusReset}}}
			dispatcher := &responseDispatcher{}
			payload := resetPayload("reset", "")
			if !test.want {
				payload.Args = "secret-selector"
			}
			payload.Access = test.access
			if err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "envelope-1"}, payload); err != nil {
				t.Fatal(err)
			}
			if got := len(lifetimes.snapshot()); (got == 1) != test.want {
				t.Fatalf("lifetime calls = %d, authorized=%t", got, test.want)
			}
			_, delivered := dispatcher.last(t)
			if !test.want && (delivered.Text != msgDenied || strings.Contains(delivered.Text, payload.Args)) {
				t.Fatalf("denied text = %q", delivered.Text)
			}
		})
	}
}

func TestHandleRejectsEveryArgumentBeforeLifetimeWork(t *testing.T) {
	for _, args := range []string{"provider=hosted", "model=secret", "--provider acp", "anything", "one two"} {
		t.Run(args, func(t *testing.T) {
			lifetimes, dispatcher := &lifetimeSubmitter{}, &responseDispatcher{}
			payload := resetPayload("new", args)
			payload.Invocation.Root = "/balda"
			if err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "envelope-1"}, payload); err != nil {
				t.Fatal(err)
			}
			if got := len(lifetimes.snapshot()); got != 0 {
				t.Fatalf("lifetime calls = %d, want 0", got)
			}
			env, delivered := dispatcher.last(t)
			if delivered.Text != "Usage: /balda new" || env.DedupeKey != "envelope-1:delivery:fresh-lifetime-usage" {
				t.Fatalf("usage response = (%q, %q)", delivered.Text, env.DedupeKey)
			}
			if strings.Contains(delivered.Text, args) {
				t.Fatalf("usage leaked arguments: %q", delivered.Text)
			}
		})
	}
}

func TestHandleSubmitsCanonicalAliasAndStableOperation(t *testing.T) {
	for _, name := range []string{"reset", "new"} {
		t.Run(name, func(t *testing.T) {
			lifetimes := &lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusReset}}}
			dispatcher := &responseDispatcher{}
			if err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "immutable-envelope"}, resetPayload(name, "  ")); err != nil {
				t.Fatal(err)
			}
			requests := lifetimes.snapshot()
			if len(requests) != 1 {
				t.Fatalf("requests = %d, want 1", len(requests))
			}
			want := lifetimeapp.Request{OperationID: lifetimeOperationID("immutable-envelope"), SessionID: resetTestSessionID, Command: name}
			if requests[0] != want {
				t.Fatalf("request = %+v, want %+v", requests[0], want)
			}
			env, delivered := dispatcher.last(t)
			if delivered.Text != msgReset || env.DedupeKey != "immutable-envelope:delivery:fresh-lifetime-reset" {
				t.Fatalf("success response = (%q, %q)", delivered.Text, env.DedupeKey)
			}
		})
	}
	if lifetimeOperationID("immutable-envelope") == lifetimeOperationID("other-envelope") {
		t.Fatal("different envelopes shared an operation id")
	}
	if got, want := lifetimeOperationID("immutable-envelope"), operationIDPrefix+"0b8ddb7afdaf8538d95c640d419f131af7fa11096b73835d79c75c7cf62146aa"; got != want {
		t.Fatalf("operation id = %q, want %q", got, want)
	}
}

func TestHandleConcurrentReplayKeepsOneOperationIdentity(t *testing.T) {
	const calls = 16
	lifetimes := &lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusReset}}}
	dispatcher := &responseDispatcher{}
	handler := New(lifetimes, dispatcher)
	var wait sync.WaitGroup
	errs := make(chan error, calls)
	for range calls {
		wait.Add(1)
		go func() {
			defer wait.Done()
			errs <- handler.Handle(t.Context(), actorlayer.Envelope{ID: "replayed-envelope"}, resetPayload("reset", ""))
		}()
	}
	wait.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	requests := lifetimes.snapshot()
	if len(requests) != calls {
		t.Fatalf("requests = %d, want %d", len(requests), calls)
	}
	for _, request := range requests {
		if request.OperationID != lifetimeOperationID("replayed-envelope") {
			t.Fatalf("operation id = %q", request.OperationID)
		}
	}
}

func TestHandleMapsBoundedSecretSafeResults(t *testing.T) {
	for _, test := range []struct {
		name   string
		status lifetimeapp.Status
		text   string
		suffix string
	}{
		{name: "reset", status: lifetimeapp.StatusReset, text: msgReset, suffix: "fresh-lifetime-reset"},
		{name: "conflict", status: lifetimeapp.StatusConflict, text: msgConflict, suffix: "fresh-lifetime-conflict"},
		{name: "unavailable", status: lifetimeapp.StatusUnavailable, text: msgUnavailable, suffix: "fresh-lifetime-unavailable"},
		{name: "interrupted", status: lifetimeapp.StatusInterrupted, text: msgInterrupted, suffix: "fresh-lifetime-interrupted"},
		{name: "recovery", status: lifetimeapp.StatusRecoveryRequired, text: msgRecoveryRequired, suffix: "fresh-lifetime-recovery-required"},
		{name: "unknown", status: "secret-target-archive", text: msgUnavailable, suffix: "fresh-lifetime-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			lifetimes := &lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: test.status, Generation: 42}}}
			dispatcher := &responseDispatcher{}
			if err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "envelope-secret"}, resetPayload("reset", "")); err != nil {
				t.Fatal(err)
			}
			env, delivered := dispatcher.last(t)
			if delivered.Text != test.text || env.DedupeKey != "envelope-secret:delivery:"+test.suffix {
				t.Fatalf("response = (%q, %q)", delivered.Text, env.DedupeKey)
			}
			for _, secret := range []string{"secret-target-archive", "binding-secret", "42"} {
				if strings.Contains(delivered.Text, secret) {
					t.Fatalf("response leaked %q: %q", secret, delivered.Text)
				}
			}
		})
	}
}

func TestHandleBoundsSubmitFailures(t *testing.T) {
	for _, lifetimes := range []*lifetimeSubmitter{
		{err: errors.New("provider/store failed with secret-binding")},
		{nilCh: true},
	} {
		dispatcher := &responseDispatcher{}
		if err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "envelope-1"}, resetPayload("reset", "")); err != nil {
			t.Fatal(err)
		}
		_, delivered := dispatcher.last(t)
		if delivered.Text != msgUnavailable || strings.Contains(delivered.Text, "secret-binding") {
			t.Fatalf("response = %q", delivered.Text)
		}
	}
}

func TestHandleCancellationUsesBoundedDetachedDelivery(t *testing.T) {
	completion := make(chan lifetimeapp.Completion)
	called := make(chan struct{})
	lifetimes := &lifetimeSubmitter{resultCh: completion, called: called}
	dispatcher := &responseDispatcher{}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		done <- New(lifetimes, dispatcher).Handle(ctx, actorlayer.Envelope{ID: "envelope-1"}, resetPayload("reset", ""))
	}()
	<-called
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, delivered := dispatcher.last(t)
	if delivered.Text != msgInterrupted {
		t.Fatalf("canceled response = %q", delivered.Text)
	}
}

func TestHandleCancellationReportsLateOutcomeAsUnconfirmed(t *testing.T) {
	for _, late := range []lifetimeapp.Status{lifetimeapp.StatusReset, lifetimeapp.StatusRecoveryRequired} {
		t.Run(string(late), func(t *testing.T) {
			completion := make(chan lifetimeapp.Completion, 1)
			called := make(chan struct{})
			lifetimes := &lifetimeSubmitter{resultCh: completion, called: called}
			dispatcher := &responseDispatcher{}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			go func() {
				done <- New(lifetimes, dispatcher).Handle(ctx, actorlayer.Envelope{ID: "envelope-late"}, resetPayload("reset", ""))
			}()
			<-called
			cancel()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			completion <- lifetimeapp.Completion{Result: lifetimeapp.Result{Status: late}}
			close(completion)

			env, delivered := dispatcher.last(t)
			if delivered.Text != msgInterrupted || env.DedupeKey != "envelope-late:delivery:fresh-lifetime-interrupted" {
				t.Fatalf("response = (%q, %q)", delivered.Text, env.DedupeKey)
			}
			for _, falseClaim := range []string{"No new reset was started", "unchanged", "did not reset"} {
				if strings.Contains(delivered.Text, falseClaim) {
					t.Fatalf("interrupted response made unsupported claim %q: %q", falseClaim, delivered.Text)
				}
			}
			if !strings.Contains(delivered.Text, "unconfirmed") || !strings.Contains(delivered.Text, "/status") {
				t.Fatalf("interrupted response lacks recovery guidance: %q", delivered.Text)
			}
		})
	}
}

func TestHandleAuthoritativeRecoveryBeatsCancellation(t *testing.T) {
	completion := make(chan lifetimeapp.Completion, 1)
	completion <- lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusRecoveryRequired}}
	close(completion)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	dispatcher := &responseDispatcher{}
	if err := New(&lifetimeSubmitter{resultCh: completion}, dispatcher).Handle(ctx, actorlayer.Envelope{ID: "envelope-1"}, resetPayload("reset", "")); err != nil {
		t.Fatal(err)
	}
	_, delivered := dispatcher.last(t)
	if delivered.Text != msgRecoveryRequired {
		t.Fatalf("response = %q, want recovery", delivered.Text)
	}
}

func TestHandleDeliveryFailureRemainsRetryableAndBounded(t *testing.T) {
	secret := errors.New("delivery failed with secret-token")
	dispatcher := &responseDispatcher{err: secret}
	lifetimes := &lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusReset}}}
	err := New(lifetimes, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "envelope-1"}, resetPayload("reset", ""))
	if err == nil || actorlayer.ClassifyError(err) != actorlayer.ErrorKindTransient || !errors.Is(err, secret) {
		t.Fatalf("error = %v, want transient", err)
	}
}

func TestHandleDetachedDeliveryTimeoutRemainsRetryable(t *testing.T) {
	blocked := make(chan struct{})
	dispatcher := &responseDispatcher{block: blocked}
	handler := New(&lifetimeSubmitter{result: lifetimeapp.Completion{Result: lifetimeapp.Result{Status: lifetimeapp.StatusReset}}}, dispatcher)
	handler.deliveryTimeout = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := handler.Handle(ctx, actorlayer.Envelope{ID: "envelope-1"}, resetPayload("reset", ""))
	if err == nil || actorlayer.ClassifyError(err) != actorlayer.ErrorKindTransient || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want transient deadline", err)
	}
}

func resetPayload(name, args string) commandcmd.Payload {
	return commandcmd.Payload{
		Version: commandcmd.SchemaVersion, Name: name, Args: args, SnapshotID: runtimecatalogcmd.SnapshotID("snapshot-1"),
		Locator:   deliverycmd.Locator{ChannelType: "telegram", AddressKey: "1:0", AddressJSON: "{}", SessionID: resetTestSessionID},
		Transport: "telegram", Principal: "tg-1", Access: commandcmd.Access{SessionCommands: true},
		Conversation: commandcmd.Conversation{Direct: true}, Invocation: commandcmd.Invocation{Root: "/"},
	}
}
