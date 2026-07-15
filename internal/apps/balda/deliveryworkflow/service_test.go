package deliveryworkflow

import (
	"context"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	"github.com/normahq/balda/internal/apps/balda/deliverycmd"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	"github.com/rs/zerolog"
)

type testDispatcher struct{}

func (testDispatcher) Dispatch(context.Context, deliverycmd.Payload) (string, error) {
	return "message-42", nil
}

type testQuestionBinder struct {
	questionID string
	ref        questioncmd.DeliveryRef
}

func (b *testQuestionBinder) BindDelivery(_ context.Context, questionID string, ref questioncmd.DeliveryRef) error {
	b.questionID = questionID
	b.ref = ref
	return nil
}

func TestHandleBindsQuestionToProviderMessage(t *testing.T) {
	binder := &testQuestionBinder{}
	service := New(testDispatcher{}, nil, nil, binder, zerolog.Nop())
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
