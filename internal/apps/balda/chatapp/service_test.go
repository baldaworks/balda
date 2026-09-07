package chatapp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/actorcmd"
	"github.com/baldaworks/balda/internal/apps/balda/attachment"
	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type stubAuthorizer struct {
	result Authorization
	err    error
	calls  int
}

func (s *stubAuthorizer) Authorize(context.Context, InboundContext) (Authorization, error) {
	s.calls++
	return s.result, s.err
}

type stubSessionPreparer struct {
	result SessionPreparation
	err    error
	calls  int
}

func (s *stubSessionPreparer) Prepare(context.Context, InboundContext) (SessionPreparation, error) {
	s.calls++
	return s.result, s.err
}

type recordingDispatcher struct {
	mu        sync.Mutex
	receipt   *actortransport.DispatchReceipt
	err       error
	envelopes []actorlayer.Envelope
}

func (d *recordingDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.envelopes = append(d.envelopes, envelope)
	return d.receipt, d.err
}

type stubQuestionResolver struct {
	result QuestionResolution
	err    error
	calls  int
}

func (s *stubQuestionResolver) ResolveQuestionReply(context.Context, questioncmd.InboundReply) (QuestionResolution, error) {
	s.calls++
	return s.result, s.err
}

func testRequest(id string, text string) Request {
	return Request{
		ID:   turncmd.InboundID(id),
		Text: text,
		Locator: deliverycmd.Locator{
			ChannelType: "telegram",
			AddressKey:  "9001:0",
			SessionID:   "s-123",
		},
		UserID:     "user-1",
		ReceivedAt: time.Now().UTC(),
		Direct:     true,
		Source:     "telegram",
	}
}

func TestService_HandleChat_NormalFlow(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "msg-1"},
	}
	sessions := &stubSessionPreparer{
		result: SessionPreparation{
			Ready:          true,
			UserID:         "user-1",
			AgentSessionID: "agent-s-1",
		},
	}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
		Logger:     zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-1", "hello balda")
	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if !res.Activated {
		t.Fatal("expected Activated = true")
	}
	if res.Settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundAccepted)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatched envelopes = %d, want 1", len(dispatcher.envelopes))
	}
}

func TestService_HandleChat_EmptyInboundTerminates(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{}
	sessions := &stubSessionPreparer{}
	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-empty", "   ")
	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if res.Activated {
		t.Fatal("expected Activated = false for empty inbound")
	}
	if res.Settlement.Outcome != turncmd.InboundTerminal {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundTerminal)
	}
	if res.Settlement.Reason != ReasonEmptyInbound {
		t.Fatalf("reason = %q, want %q", res.Settlement.Reason, ReasonEmptyInbound)
	}
	if sessions.calls != 0 {
		t.Fatalf("sessions called = %d, want 0", sessions.calls)
	}
}

func TestService_HandleChat_AuthorizationRejectionTerminates(t *testing.T) {
	t.Parallel()

	authorizer := &stubAuthorizer{
		result: Authorization{Allowed: false, Reason: ReasonUnauthorized},
	}
	sessions := &stubSessionPreparer{}
	dispatcher := &recordingDispatcher{}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
		Authorizer: authorizer,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	res, err := service.HandleChat(context.Background(), testRequest("in-denied", "denied message"))
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if res.Activated {
		t.Fatal("expected Activated = false")
	}
	if res.Settlement.Outcome != turncmd.InboundTerminal {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundTerminal)
	}
	if sessions.calls != 0 {
		t.Fatalf("sessions called = %d, want 0", sessions.calls)
	}
}

func TestService_HandleChat_SessionPreparationFailure(t *testing.T) {
	t.Parallel()

	transientErr := actorlayer.TransientError(errors.New("db timeout"))
	sessions := &stubSessionPreparer{
		err: transientErr,
	}
	dispatcher := &recordingDispatcher{}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	res, err := service.HandleChat(context.Background(), testRequest("in-sess-fail", "message"))
	if err == nil {
		t.Fatal("expected error from failed session preparation")
	}
	if res.Settlement.Outcome != turncmd.InboundRetry {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundRetry)
	}
	if res.Activated {
		t.Fatal("expected Activated = false")
	}
}

func TestService_HandleChat_DispatchError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		dispatchErr error
		wantOutcome turncmd.InboundOutcome
	}{
		{
			name:        "transient error retries",
			dispatchErr: actorlayer.TransientError(errors.New("network")),
			wantOutcome: turncmd.InboundRetry,
		},
		{
			name:        "policy error terminates",
			dispatchErr: actorlayer.PolicyError(errors.New("forbidden")),
			wantOutcome: turncmd.InboundTerminal,
		},
		{
			name:        "command queue full retries",
			dispatchErr: actorcmd.ErrCommandQueueFull,
			wantOutcome: turncmd.InboundRetry,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			sessions := &stubSessionPreparer{
				result: SessionPreparation{Ready: true, UserID: "user-1"},
			}
			dispatcher := &recordingDispatcher{err: tc.dispatchErr}

			service, err := NewService(ServiceParams{
				Sessions:   sessions,
				Dispatcher: dispatcher,
			})
			if err != nil {
				t.Fatalf("NewService error: %v", err)
			}

			res, err := service.HandleChat(context.Background(), testRequest("in-disp", "test"))
			if err == nil {
				t.Fatal("expected error from dispatch failure")
			}
			if res.Settlement.Outcome != tc.wantOutcome {
				t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, tc.wantOutcome)
			}
			if res.Activated {
				t.Fatal("expected Activated = false")
			}
		})
	}
}

func TestService_HandleChat_QuestionReply_SettledActivates(t *testing.T) {
	t.Parallel()

	continuationEnv := actorlayer.Envelope{
		ID:        "env-cont-1",
		Namespace: "session",
		Kind:      "continuation",
	}
	questionResolver := &stubQuestionResolver{
		result: QuestionResolution{
			Matched:      true,
			Settled:      true,
			Continuation: continuationEnv,
		},
	}
	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "msg-q"},
	}
	sessions := &stubSessionPreparer{}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
		Questions:  questionResolver,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-q1", "1")
	req.QuestionReply = &questioncmd.InboundReply{
		ReplyToMessageID: "1001",
		Text:             "1",
	}

	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if !res.Activated {
		t.Fatal("expected Activated = true for settled question reply")
	}
	if res.Settlement.Outcome != turncmd.InboundTerminal {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundTerminal)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(dispatcher.envelopes))
	}
	if dispatcher.envelopes[0].DedupeKey != "in-q1" {
		t.Fatalf("dedupeKey = %q, want %q", dispatcher.envelopes[0].DedupeKey, "in-q1")
	}
	// Session preparer must not be called when handled via question
	if sessions.calls != 0 {
		t.Fatalf("sessions called = %d, want 0", sessions.calls)
	}
}

func TestService_HandleChat_QuestionReply_UnsettledTerminatesWithoutActivation(t *testing.T) {
	t.Parallel()

	questionResolver := &stubQuestionResolver{
		result: QuestionResolution{
			Matched: true,
			Settled: false, // e.g. already answered or expired
		},
	}
	dispatcher := &recordingDispatcher{}
	sessions := &stubSessionPreparer{}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
		Questions:  questionResolver,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-q2", "1")
	req.QuestionReply = &questioncmd.InboundReply{ReplyToMessageID: "1002"}

	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if res.Activated {
		t.Fatal("expected Activated = false for unsettled question reply")
	}
	if res.Settlement.Outcome != turncmd.InboundTerminal {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundTerminal)
	}
	if len(dispatcher.envelopes) != 0 {
		t.Fatalf("dispatched = %d, want 0", len(dispatcher.envelopes))
	}
}

func TestService_HandleChat_QuestionReply_UnmatchedFallsThrough(t *testing.T) {
	t.Parallel()

	questionResolver := &stubQuestionResolver{
		result: QuestionResolution{
			Matched: false,
		},
	}
	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "msg-norm"},
	}
	sessions := &stubSessionPreparer{
		result: SessionPreparation{Ready: true, UserID: "user-1"},
	}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
		Questions:  questionResolver,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-q3", "regular text after unrecognised reply")
	req.QuestionReply = &questioncmd.InboundReply{ReplyToMessageID: "non-existent"}

	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if !res.Activated {
		t.Fatal("expected Activated = true via fallback")
	}
	if res.Settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundAccepted)
	}
	if sessions.calls != 1 {
		t.Fatalf("sessions called = %d, want 1", sessions.calls)
	}
	if len(dispatcher.envelopes) != 1 {
		t.Fatalf("dispatched = %d, want 1", len(dispatcher.envelopes))
	}
}

func TestService_HandleChat_AttachmentsOnly(t *testing.T) {
	t.Parallel()

	dispatcher := &recordingDispatcher{
		receipt: &actortransport.DispatchReceipt{MsgID: "msg-att"},
	}
	sessions := &stubSessionPreparer{
		result: SessionPreparation{Ready: true, UserID: "user-1"},
	}

	service, err := NewService(ServiceParams{
		Sessions:   sessions,
		Dispatcher: dispatcher,
	})
	if err != nil {
		t.Fatalf("NewService error: %v", err)
	}

	req := testRequest("in-att", "")
	req.Attachments = []attachment.Descriptor{
		{
			Kind:     attachment.KindDocument,
			FileID:   "file-1",
			FileName: "file.txt",
			MIMEType: "text/plain",
		},
	}

	res, err := service.HandleChat(context.Background(), req)
	if err != nil {
		t.Fatalf("HandleChat error: %v", err)
	}
	if !res.Activated {
		t.Fatal("expected Activated = true for attachment-only message")
	}
	if res.Settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("outcome = %q, want %q", res.Settlement.Outcome, turncmd.InboundAccepted)
	}
}
