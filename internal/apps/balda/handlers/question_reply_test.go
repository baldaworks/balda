package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	baldatelegram "github.com/normahq/balda/internal/apps/balda/channel/telegram"
	"github.com/normahq/balda/internal/apps/balda/questioncmd"
	"github.com/normahq/balda/internal/apps/balda/questions"
	baldasession "github.com/normahq/balda/internal/apps/balda/session"
	baldastate "github.com/normahq/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
)

type fakeQuestionStore struct {
	record baldastate.QuestionRecord
}

func (f *fakeQuestionStore) CreatePendingQuestion(context.Context, baldastate.QuestionRecord) error {
	return nil
}
func (f *fakeQuestionStore) BindQuestionDeliveryRef(context.Context, string, questioncmd.DeliveryRef) error {
	return nil
}
func (f *fakeQuestionStore) GetQuestionByID(context.Context, string) (baldastate.QuestionRecord, bool, error) {
	return f.record, true, nil
}
func (f *fakeQuestionStore) GetPendingQuestionByReplyRef(_ context.Context, provider, conversationKey, replyToMessageID string) (baldastate.QuestionRecord, bool, error) {
	if provider == f.record.Provider && conversationKey == f.record.AddressKey && replyToMessageID == f.record.ProviderMessageID {
		return f.record, true, nil
	}
	return baldastate.QuestionRecord{}, false, nil
}
func (f *fakeQuestionStore) MarkQuestionAnswered(_ context.Context, questionID string, answer questioncmd.Answer) (baldastate.QuestionRecord, bool, error) {
	if questionID != f.record.QuestionID {
		return baldastate.QuestionRecord{}, false, nil
	}
	f.record.Status = questioncmd.StatusAnswered
	f.record.AnswerJSON = `{"text":"` + answer.Text + `"}`
	return f.record, true, nil
}
func (f *fakeQuestionStore) MarkQuestionTimedOut(context.Context, string, time.Time) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}

func TestHandleQuestionReplyEnqueuesContinuationTurn(t *testing.T) {
	store := &fakeQuestionStore{
		record: baldastate.QuestionRecord{
			QuestionID:        "question-1",
			SessionID:         "tg-1-0",
			AddressKey:        "1:0",
			Provider:          "telegram",
			ConversationKey:   "1:0",
			ProviderMessageID: "42",
			Status:            questioncmd.StatusPending,
			InteractionJSON:   `{"session_id":"tg-1-0","channel_kind":"telegram","locator":{"session_id":"tg-1-0","channel_type":"telegram","address_key":"1:0","address_json":"{\"chat_id\":1,\"topic_id\":0}"}}`,
			ResumeJSON:        `{"to":"session:tg-1-0"}`,
		},
	}
	dispatcher := &fakeTurnDispatcher{}
	handler := &BaldaHandler{
		actorDispatcher: dispatcher,
		questionService: questions.New(store, nil, zerolog.Nop()),
		now:             func() time.Time { return time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC) },
	}
	handled, err := handler.handleQuestionReply(context.Background(), baldatelegram.MessageContext{
		Locator:          baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`},
		TopicID:          0,
		MessageID:        43,
		ReplyToMessageID: 42,
		UserID:           101,
	}, "да")
	if err != nil {
		t.Fatalf("handleQuestionReply() error = %v", err)
	}
	if !handled {
		t.Fatal("handleQuestionReply() handled = false, want true")
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
	var payload questioncmd.AnsweredContinuation
	if err := actorlayer.UnmarshalPayload(dispatcher.commands[0].Payload, &payload); err != nil {
		t.Fatalf("decode dispatched payload: %v", err)
	}
	if payload.QuestionID != "question-1" {
		t.Fatalf("question_id = %q, want question-1", payload.QuestionID)
	}
	if payload.Answer.Text != "да" {
		t.Fatalf("answer text = %q, want да", payload.Answer.Text)
	}
}
