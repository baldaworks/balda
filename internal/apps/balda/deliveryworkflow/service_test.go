package deliveryworkflow

import (
	"context"
	"errors"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	"github.com/rs/zerolog"
)

type testDispatcher struct{}

func (testDispatcher) Dispatch(context.Context, deliverycmd.Payload) (string, error) {
	return "message-42", nil
}

type failingDeliveryDispatcher struct {
	calls int
	err   error
}

func (d *failingDeliveryDispatcher) Dispatch(context.Context, deliverycmd.Payload) (string, error) {
	d.calls++
	if d.err != nil {
		return "", d.err
	}
	return "", errors.New("ephemeral delivery rejected")
}

type failedQuestionBinder struct {
	status  string
	failure questioncmd.Failure
}

func (*failedQuestionBinder) BindDelivery(context.Context, string, questioncmd.DeliveryRef) error {
	return nil
}

func (b *failedQuestionBinder) DeliveryState(context.Context, string) (string, bool, error) {
	status := b.status
	if status == "" {
		status = questioncmd.StatusPending
	}
	return status, true, nil
}

func (*failedQuestionBinder) FailedDeliveryContinuation(context.Context, string) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (b *failedQuestionBinder) FailDelivery(_ context.Context, _ string, failure questioncmd.Failure) (actorlayer.Envelope, bool, error) {
	b.failure = failure
	return actorlayer.Envelope{ID: "question-failed", DedupeKey: "question:question-1:failed"}, true, nil
}

type recordingActorDispatcher struct{ envelopes []actorlayer.Envelope }

func (d *recordingActorDispatcher) Dispatch(_ context.Context, envelope actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	d.envelopes = append(d.envelopes, envelope)
	return &actortransport.DispatchReceipt{}, nil
}

func TestHandleFailsQuestionClosedWhenProviderDeliveryFails(t *testing.T) {
	delivery := &failingDeliveryDispatcher{}
	binder := &failedQuestionBinder{}
	actor := &recordingActorDispatcher{}
	service := New(delivery, nil, nil, binder, actor, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator:  deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:     deliverycmd.ModeAgentReply,
		Refs:     map[string]string{"question_id": "question-1"},
		Question: &deliverycmd.Question{ID: "question-1", Options: []deliverycmd.QuestionOption{{ID: "deny", Label: "Deny"}}},
		Text:     "permission?",
	}
	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.calls != 1 || binder.failure.Code != "delivery_failed" {
		t.Fatalf("delivery calls = %d failure = %+v", delivery.calls, binder.failure)
	}
	if len(actor.envelopes) != 1 || actor.envelopes[0].DedupeKey != "question:question-1:failed" {
		t.Fatalf("continuations = %+v", actor.envelopes)
	}
}

func TestHandleRetriesQuestionWhenProviderDeliveryFailureIsRetryable(t *testing.T) {
	delivery := &failingDeliveryDispatcher{err: deliverycmd.RetryableError(errors.New("telegram timeout"))}
	binder := &failedQuestionBinder{}
	actor := &recordingActorDispatcher{}
	service := New(delivery, nil, nil, binder, actor, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator:  deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:     deliverycmd.ModeAgentReply,
		Refs:     map[string]string{"question_id": "question-1"},
		Question: &deliverycmd.Question{ID: "question-1", Options: []deliverycmd.QuestionOption{{ID: "deny", Label: "Deny"}}},
		Text:     "permission?",
	}

	err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload)
	if actorlayer.ClassifyError(err) != actorlayer.ErrorKindExternalDelivery {
		t.Fatalf("Handle() error kind = %q, want external delivery: %v", actorlayer.ClassifyError(err), err)
	}
	if delivery.calls != 1 {
		t.Fatalf("delivery calls = %d, want 1", delivery.calls)
	}
	if binder.failure.Code != "" {
		t.Fatalf("failure = %+v, want question left pending", binder.failure)
	}
	if len(actor.envelopes) != 0 {
		t.Fatalf("continuations = %+v, want none before retry exhaustion or timeout", actor.envelopes)
	}
}

func TestHandleSuppressesLateQuestionDeliveryAfterSettlement(t *testing.T) {
	delivery := &failingDeliveryDispatcher{}
	binder := &failedQuestionBinder{status: questioncmd.StatusTimedOut}
	service := New(delivery, nil, nil, binder, nil, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator: deliverycmd.Locator{ChannelType: "telegram", AddressKey: "-1001:0", AddressJSON: `{"chat_id":-1001,"topic_id":0}`, SessionID: "tg--1001-0"},
		Mode:    deliverycmd.ModeAgentReply,
		Refs:    map[string]string{"question_id": "question-1"},
		Text:    "permission?",
	}

	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if delivery.calls != 0 {
		t.Fatalf("delivery calls = %d, want 0 after question settlement", delivery.calls)
	}
}

type testQuestionBinder struct {
	questionID string
	ref        questioncmd.DeliveryRef
}

func (*testQuestionBinder) DeliveryState(context.Context, string) (string, bool, error) {
	return questioncmd.StatusPending, true, nil
}

func (*testQuestionBinder) FailedDeliveryContinuation(context.Context, string) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (*testQuestionBinder) FailDelivery(context.Context, string, questioncmd.Failure) (actorlayer.Envelope, bool, error) {
	return actorlayer.Envelope{}, false, nil
}

func (b *testQuestionBinder) BindDelivery(_ context.Context, questionID string, ref questioncmd.DeliveryRef) error {
	b.questionID = questionID
	b.ref = ref
	return nil
}

func TestHandleBindsQuestionToProviderMessage(t *testing.T) {
	binder := &testQuestionBinder{}
	service := New(testDispatcher{}, nil, nil, binder, nil, zerolog.Nop())
	payload := deliverycmd.Payload{
		Locator: deliverycmd.Locator{
			ChannelType: "telegram",
			AddressKey:  "1:0",
			AddressJSON: `{"chat_id":1,"topic_id":0}`,
			SessionID:   "tg-1-0",
		},
		Mode:       deliverycmd.ModeAgentReply,
		Settlement: deliverycmd.SettlementOutbox,
		Refs:       map[string]string{"question_id": "question-1"},
		Text:       "permission?",
	}
	if err := service.Handle(context.Background(), actorlayer.Envelope{ID: "delivery-1"}, payload); err != nil {
		t.Fatalf("Handle() error = %v", err)
	}
	if binder.questionID != "question-1" || binder.ref.ProviderMessageID != "message-42" || binder.ref.Provider != "telegram" {
		t.Fatalf("binding = %q %+v", binder.questionID, binder.ref)
	}
}
