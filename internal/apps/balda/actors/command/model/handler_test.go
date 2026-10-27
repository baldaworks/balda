package model

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/modelapp"
	"github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
)

const testSessionID = "session-a"

type testSessionReader struct {
	mu    sync.Mutex
	info  session.TopicSessionInfo
	err   error
	calls []string
}

func (r *testSessionReader) GetSessionInfo(_ context.Context, sessionID string) (session.TopicSessionInfo, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls = append(r.calls, sessionID)
	return r.info, r.err
}

func (r *testSessionReader) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

type testSubmitter struct {
	mu        sync.Mutex
	requests  []modelapp.Request
	result    modelapp.Completion
	resultCh  <-chan modelapp.Completion
	nilResult bool
	err       error
}

func (s *testSubmitter) Submit(_ context.Context, request modelapp.Request) (<-chan modelapp.Completion, int, error) {
	s.mu.Lock()
	s.requests = append(s.requests, request)
	resultCh, result, nilResult, err := s.resultCh, s.result, s.nilResult, s.err
	s.mu.Unlock()
	if err != nil {
		return nil, 0, err
	}
	if nilResult {
		return nil, 1, nil
	}
	if resultCh != nil {
		return resultCh, 1, nil
	}
	completion := make(chan modelapp.Completion, 1)
	completion <- result
	close(completion)
	return completion, 1, nil
}

func (s *testSubmitter) snapshot() []modelapp.Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]modelapp.Request(nil), s.requests...)
}

type testDispatcher struct {
	mu        sync.Mutex
	envelopes []actorlayer.Envelope
	contexts  []context.Context
	block     <-chan struct{}
	err       error
}

func (d *testDispatcher) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
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

func (d *testDispatcher) last(t *testing.T) (actorlayer.Envelope, deliverycmd.Payload) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.envelopes) == 0 {
		t.Fatal("no delivery envelope")
	}
	env := d.envelopes[len(d.envelopes)-1]
	var payload deliverycmd.Payload
	if err := json.Unmarshal(env.Payload.Data, &payload); err != nil {
		t.Fatalf("decode delivery: %v", err)
	}
	return env, payload
}

func (d *testDispatcher) lastContext(t *testing.T) context.Context {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.contexts) == 0 {
		t.Fatal("no delivery context")
	}
	return d.contexts[len(d.contexts)-1]
}

func TestHandleAuthorizesBeforeProtectedWork(t *testing.T) {
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
			sessions := &testSessionReader{info: session.TopicSessionInfo{Model: "model-current", Recovery: sessionRecoveryReady}}
			models := &testSubmitter{}
			dispatcher := &testDispatcher{}
			payload := modelPayload("")
			payload.Access = test.access
			if err := New(sessions, models, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "op-1"}, payload); err != nil {
				t.Fatal(err)
			}
			if got := sessions.callCount(); (got == 1) != test.want {
				t.Fatalf("session calls = %d, authorized=%t", got, test.want)
			}
			_, delivered := dispatcher.last(t)
			if !test.want && delivered.Text != msgDenied {
				t.Fatalf("denied text = %q", delivered.Text)
			}
			if len(models.snapshot()) != 0 {
				t.Fatal("bare command submitted a model operation")
			}
		})
	}
}

func TestHandleRejectsMalformedTargetWithoutProtectedWork(t *testing.T) {
	for _, args := range []string{"model-a model-b", "model=model-a", "provider=model-a", "--model"} {
		t.Run(args, func(t *testing.T) {
			sessions, models, dispatcher := &testSessionReader{}, &testSubmitter{}, &testDispatcher{}
			if err := New(sessions, models, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "op-1"}, modelPayload(args)); err != nil {
				t.Fatal(err)
			}
			if sessions.callCount() != 0 || len(models.snapshot()) != 0 {
				t.Fatal("malformed command reached protected work")
			}
			env, delivered := dispatcher.last(t)
			if delivered.Text != "Usage: /model [model-id]" || env.DedupeKey != "op-1:delivery:model-usage" {
				t.Fatalf("usage delivery = %q, %q", delivered.Text, env.DedupeKey)
			}
		})
	}
}

func TestHandleReadsOnlyCommittedModel(t *testing.T) {
	for _, test := range []struct {
		name string
		info session.TopicSessionInfo
		err  error
		text string
	}{
		{name: "current", info: session.TopicSessionInfo{Model: "model-current", Recovery: sessionRecoveryReady}, text: "Current model: model-current."},
		{name: "missing model", info: session.TopicSessionInfo{Recovery: sessionRecoveryReady}, text: msgUnavailable},
		{name: "lookup failed", err: errors.New("secret database failure"), text: msgUnavailable},
		{name: "recovery error", err: session.ErrRuntimeRecoveryRequired, text: msgRecoveryRequired},
		{name: "recovery status", info: session.TopicSessionInfo{Status: sessionRecoveryRequired, Model: "secret-model"}, text: msgRecoveryRequired},
		{name: "recovery marker", info: session.TopicSessionInfo{Recovery: "ambiguous", Model: "secret-model"}, text: msgRecoveryRequired},
	} {
		t.Run(test.name, func(t *testing.T) {
			sessions := &testSessionReader{info: test.info, err: test.err}
			models, dispatcher := &testSubmitter{}, &testDispatcher{}
			if err := New(sessions, models, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "op-1"}, modelPayload("  \t ")); err != nil {
				t.Fatal(err)
			}
			if sessions.callCount() != 1 || len(models.snapshot()) != 0 {
				t.Fatalf("calls: read=%d submit=%d", sessions.callCount(), len(models.snapshot()))
			}
			_, delivered := dispatcher.last(t)
			if delivered.Text != test.text || strings.Contains(delivered.Text, "secret") {
				t.Fatalf("delivery text = %q, want %q", delivered.Text, test.text)
			}
		})
	}
}

func TestHandleMapsEveryModelResult(t *testing.T) {
	for _, test := range []struct {
		name   string
		result session.ModelChangeResult
		err    error
		text   string
		suffix string
	}{
		{name: "changed live", result: session.ModelChangeResult{Status: session.ModelChangeChangedLive, EffectiveModel: "model-next"}, text: "Model changed live to model-next.", suffix: "model-changed-live"},
		{name: "changed replacement", result: session.ModelChangeResult{Status: session.ModelChangeChangedReplacement, EffectiveModel: "model-next"}, text: "Model changed to model-next while preserving this session and its context.", suffix: "model-changed-replacement"},
		{name: "live missing effective model", result: session.ModelChangeResult{Status: session.ModelChangeChangedLive}, text: msgUnavailable, suffix: "model-unavailable"},
		{name: "replacement missing effective model", result: session.ModelChangeResult{Status: session.ModelChangeChangedReplacement}, text: msgUnavailable, suffix: "model-unavailable"},
		{name: "unsupported", result: session.ModelChangeResult{Status: session.ModelChangeUnsupported}, text: msgUnsupported, suffix: "model-unsupported"},
		{name: "incompatible", result: session.ModelChangeResult{Status: session.ModelChangeIncompatible}, text: msgIncompatible, suffix: "model-incompatible"},
		{name: "rejected", result: session.ModelChangeResult{Status: session.ModelChangeRejected}, text: msgRejected, suffix: "model-rejected"},
		{name: "busy", result: session.ModelChangeResult{Status: session.ModelChangeBusy}, text: msgBusy, suffix: "model-busy"},
		{name: "conflict", result: session.ModelChangeResult{Status: session.ModelChangeConflict}, text: msgConflict, suffix: "model-conflict"},
		{name: "unavailable", result: session.ModelChangeResult{Status: session.ModelChangeUnavailable}, text: msgUnavailable, suffix: "model-unavailable"},
		{name: "recovery", result: session.ModelChangeResult{Status: session.ModelChangeRecoveryRequired}, text: msgRecoveryRequired, suffix: "model-recovery-required"},
		{name: "recovery beats error", result: session.ModelChangeResult{Status: session.ModelChangeRecoveryRequired}, err: errors.New("secret provider error"), text: msgRecoveryRequired, suffix: "model-recovery-required"},
		{name: "error without recovery", result: session.ModelChangeResult{Status: session.ModelChangeRejected}, err: errors.New("secret provider error"), text: msgUnavailable, suffix: "model-unavailable"},
		{name: "unknown", result: session.ModelChangeResult{Status: "future", EffectiveModel: "secret-model"}, text: msgUnavailable, suffix: "model-unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			models := &testSubmitter{result: modelapp.Completion{Result: test.result, Err: test.err}}
			dispatcher := &testDispatcher{}
			if err := New(&testSessionReader{}, models, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "op-1"}, modelPayload("target-secret")); err != nil {
				t.Fatal(err)
			}
			env, delivered := dispatcher.last(t)
			if delivered.Text != test.text || env.DedupeKey != "op-1:delivery:"+test.suffix {
				t.Fatalf("delivery = %q, %q", delivered.Text, env.DedupeKey)
			}
			if strings.Contains(delivered.Text, "target-secret") || strings.Contains(delivered.Text, "provider error") {
				t.Fatalf("delivery leaked protected input: %q", delivered.Text)
			}
			requests := models.snapshot()
			if len(requests) != 1 || requests[0].SessionID != testSessionID || requests[0].Model != "target-secret" {
				t.Fatalf("request = %+v", requests)
			}
		})
	}
}

func TestHandleMapsSubmissionFailuresAndCanceledCompletion(t *testing.T) {
	for _, test := range []struct {
		name   string
		models *testSubmitter
		text   string
	}{
		{name: "queue conflict", models: &testSubmitter{err: modelapp.ErrConflict}, text: msgConflict},
		{name: "service unavailable", models: &testSubmitter{err: errors.New("secret admission failure")}, text: msgUnavailable},
		{name: "nil completion", models: &testSubmitter{nilResult: true}, text: msgUnavailable},
		{name: "closed completion", models: &testSubmitter{resultCh: closedCompletion()}, text: msgUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &testDispatcher{}
			if err := New(&testSessionReader{}, test.models, dispatcher).Handle(t.Context(), actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next")); err != nil {
				t.Fatal(err)
			}
			_, delivered := dispatcher.last(t)
			if delivered.Text != test.text || strings.Contains(delivered.Text, "secret") {
				t.Fatalf("delivery = %q", delivered.Text)
			}
		})
	}

	waiting := make(chan modelapp.Completion)
	models, dispatcher := &testSubmitter{resultCh: waiting}, &testDispatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := New(&testSessionReader{}, models, dispatcher).Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next")); err != nil {
		t.Fatal(err)
	}
	_, delivered := dispatcher.last(t)
	if delivered.Text != msgUnavailable {
		t.Fatalf("canceled delivery = %q", delivered.Text)
	}
}

func TestModelOperationIDIsVersionedBoundedAndStable(t *testing.T) {
	const want = "model-command:v1:71a0ef7195df486543f63f0cb1dff83ee245c10fb566219b128a7fa99a8bbbac"
	if got := modelOperationID("op-1"); got != want {
		t.Fatalf("modelOperationID() = %q, want %q", got, want)
	}
	if got := modelOperationID("op-2"); got == want {
		t.Fatal("different envelope IDs collided")
	}
	if len(want) > 128 {
		t.Fatalf("operation ID length = %d", len(want))
	}
}

func TestConcurrentRedeliveryUsesOneSemanticOperation(t *testing.T) {
	models := &deduplicatingSubmitter{seen: make(map[string]modelapp.Completion)}
	dispatcher := &testDispatcher{}
	handler := New(&testSessionReader{}, models, dispatcher)
	const calls = 32
	var wg sync.WaitGroup
	for range calls {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := handler.Handle(context.Background(), actorlayer.Envelope{ID: "same-envelope"}, modelPayload("model-next")); err != nil {
				t.Errorf("Handle() = %v", err)
			}
		}()
	}
	wg.Wait()
	if models.mutations.Load() != 1 {
		t.Fatalf("semantic mutations = %d, want 1", models.mutations.Load())
	}
	if models.wrongSession.Load() != 0 {
		t.Fatalf("wrong-session submissions = %d", models.wrongSession.Load())
	}
}

type deduplicatingSubmitter struct {
	mu           sync.Mutex
	seen         map[string]modelapp.Completion
	mutations    atomic.Int32
	wrongSession atomic.Int32
}

func (s *deduplicatingSubmitter) Submit(_ context.Context, request modelapp.Request) (<-chan modelapp.Completion, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if request.SessionID != testSessionID {
		s.wrongSession.Add(1)
	}
	completion, ok := s.seen[request.OperationID]
	if !ok {
		s.mutations.Add(1)
		completion = modelapp.Completion{Result: session.ModelChangeResult{Status: session.ModelChangeChangedLive, EffectiveModel: "model-next"}}
		s.seen[request.OperationID] = completion
	}
	result := make(chan modelapp.Completion, 1)
	result <- completion
	close(result)
	return result, 0, nil
}

func modelPayload(args string) commandcmd.Payload {
	return commandcmd.Payload{
		Version: commandcmd.SchemaVersion,
		Name:    commandName,
		Args:    args,
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "chat:topic", AddressJSON: `{}`, SessionID: testSessionID},
		Access:  commandcmd.Access{SessionCommands: true},
	}
}

func closedCompletion() <-chan modelapp.Completion {
	completion := make(chan modelapp.Completion)
	close(completion)
	return completion
}

func TestHandleCanceledCompletionDoesNotWait(t *testing.T) {
	waiting := make(chan modelapp.Completion)
	models, dispatcher := &testSubmitter{resultCh: waiting}, &testDispatcher{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- New(&testSessionReader{}, models, dispatcher).Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next"))
	}()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("Handle did not stop after cancellation")
	}
}

func TestCancellationPrefersReadyRecoveryAndUsesBoundedDetachedDelivery(t *testing.T) {
	completion := make(chan modelapp.Completion, 1)
	completion <- modelapp.Completion{
		Result: session.ModelChangeResult{Status: session.ModelChangeRecoveryRequired},
		Err:    errors.New("secret provider uncertainty"),
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatcher := &testDispatcher{}
	handler := New(&testSessionReader{}, &testSubmitter{resultCh: completion}, dispatcher)
	handler.deliveryTimeout = 100 * time.Millisecond
	if err := handler.Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("target-secret")); err != nil {
		t.Fatal(err)
	}
	_, delivered := dispatcher.last(t)
	if delivered.Text != msgRecoveryRequired || strings.Contains(delivered.Text, "secret") {
		t.Fatalf("delivery = %q", delivered.Text)
	}
	deliveryCtx := dispatcher.lastContext(t)
	select {
	case <-deliveryCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("detached delivery context was not canceled after dispatch")
	}
}

func TestCanceledContextCanDeliverUnavailable(t *testing.T) {
	completion := make(chan modelapp.Completion, 1)
	completion <- modelapp.Completion{Err: context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dispatcher := &testDispatcher{}
	handler := New(&testSessionReader{}, &testSubmitter{resultCh: completion}, dispatcher)
	handler.deliveryTimeout = 100 * time.Millisecond
	if err := handler.Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next")); err != nil {
		t.Fatal(err)
	}
	_, delivered := dispatcher.last(t)
	if delivered.Text != msgUnavailable {
		t.Fatalf("delivery = %q", delivered.Text)
	}
}

func TestDetachedDeliveryTimeoutAndFailureRemainRetryable(t *testing.T) {
	t.Run("timeout", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dispatcher := &testDispatcher{block: make(chan struct{})}
		handler := New(&testSessionReader{}, &testSubmitter{result: modelapp.Completion{Result: session.ModelChangeResult{Status: session.ModelChangeUnavailable}}}, dispatcher)
		handler.deliveryTimeout = 10 * time.Millisecond
		started := time.Now()
		err := handler.Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next"))
		if !actorlayer.IsRetryableError(err) || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("Handle() = %v, want retryable deadline", err)
		}
		if elapsed := time.Since(started); elapsed > time.Second {
			t.Fatalf("detached delivery elapsed = %v", elapsed)
		}
	})

	t.Run("dispatcher failure", func(t *testing.T) {
		wantErr := errors.New("secret dispatch failure")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		dispatcher := &testDispatcher{err: wantErr}
		handler := New(&testSessionReader{}, &testSubmitter{result: modelapp.Completion{Result: session.ModelChangeResult{Status: session.ModelChangeUnavailable}}}, dispatcher)
		err := handler.Handle(ctx, actorlayer.Envelope{ID: "op-1"}, modelPayload("model-next"))
		if !actorlayer.IsRetryableError(err) || !errors.Is(err, wantErr) {
			t.Fatalf("Handle() = %v, want retryable dispatcher failure", err)
		}
	})
}
