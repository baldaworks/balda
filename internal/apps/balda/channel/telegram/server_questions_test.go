package telegram

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/auth"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/rs/zerolog"
	"github.com/tgbotkit/client"
	"github.com/tgbotkit/runtime/events"
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
	encoded, err := json.Marshal(answer)
	if err != nil {
		return baldastate.QuestionRecord{}, false, err
	}
	f.record.AnswerJSON = string(encoded)
	return f.record, true, nil
}
func (f *fakeQuestionStore) MarkQuestionTimedOut(context.Context, string, time.Time) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}

type fakeOwnerKVStore struct{ value any }

func (s *fakeOwnerKVStore) GetJSON(_ context.Context, _ string) (any, bool, error) {
	if s.value == nil {
		return nil, false, nil
	}
	return s.value, true, nil
}
func (s *fakeOwnerKVStore) SetJSON(_ context.Context, _ string, value any) error {
	s.value = value
	return nil
}

type fakeCollaboratorBackingStore struct{ byUserID map[string]auth.Collaborator }

func (s *fakeCollaboratorBackingStore) AddCollaborator(_ context.Context, c auth.Collaborator) error {
	if s.byUserID == nil {
		s.byUserID = map[string]auth.Collaborator{}
	}
	s.byUserID[c.UserID] = c
	return nil
}
func (s *fakeCollaboratorBackingStore) RemoveCollaborator(_ context.Context, userID string) error {
	delete(s.byUserID, userID)
	return nil
}
func (s *fakeCollaboratorBackingStore) GetCollaborator(_ context.Context, userID string) (*auth.Collaborator, bool, error) {
	c, ok := s.byUserID[userID]
	if !ok {
		return nil, false, nil
	}
	return &c, true, nil
}
func (s *fakeCollaboratorBackingStore) ListCollaborators(_ context.Context) ([]auth.Collaborator, error) {
	out := make([]auth.Collaborator, 0, len(s.byUserID))
	for _, c := range s.byUserID {
		out = append(out, c)
	}
	return out, nil
}

type callbackMessenger struct {
	TelegramMessenger
	answers []string
	alerts  []bool
}

func (m *callbackMessenger) AnswerCallbackQuery(_ context.Context, _ string, text string, showAlert bool) error {
	m.answers = append(m.answers, text)
	m.alerts = append(m.alerts, showAlert)
	return nil
}
func (m *callbackMessenger) SendPhotoByFileID(context.Context, int64, string, string, int) error { return nil }
func (m *callbackMessenger) SendDocumentByFileID(context.Context, int64, string, string, string, int) error {
	return nil
}
func (m *callbackMessenger) SendPhotoByPath(context.Context, int64, string, string, int) error { return nil }
func (m *callbackMessenger) SendDocumentByPath(context.Context, int64, string, string, string, string, int) error {
	return nil
}

type fakeTurnDispatcher struct {
	commandsMu    sync.Mutex
	commands      []actorlayer.Envelope
	commandSignal chan struct{}
}

func (f *fakeTurnDispatcher) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	f.commandsMu.Lock()
	f.commands = append(f.commands, env)
	f.commandsMu.Unlock()
	if f.commandSignal != nil {
		select {
		case f.commandSignal <- struct{}{}:
		default:
		}
	}
	return &actortransport.DispatchReceipt{}, nil
}

func (f *fakeTurnDispatcher) commandSnapshot() []actorlayer.Envelope {
	f.commandsMu.Lock()
	defer f.commandsMu.Unlock()
	return append([]actorlayer.Envelope(nil), f.commands...)
}

func questionCallbackAuthStores(t *testing.T) (*auth.OwnerStore, *auth.CollaboratorStore) {
	t.Helper()
	ownerStore, err := auth.NewOwnerStore(&fakeOwnerKVStore{})
	if err != nil {
		t.Fatalf("NewOwnerStore() error = %v", err)
	}
	if _, err := ownerStore.RegisterOwner(101, 1); err != nil {
		t.Fatalf("RegisterOwner() error = %v", err)
	}
	return ownerStore, auth.NewCollaboratorStore(&fakeCollaboratorBackingStore{})
}

func callbackQuestionRecord(status string) baldastate.QuestionRecord {
	return baldastate.QuestionRecord{
		QuestionID:        "question-1",
		SessionID:         "tg-1-0",
		AddressKey:        "1:0",
		Provider:          "telegram",
		ConversationKey:   "1:0",
		ProviderMessageID: "42",
		Status:            status,
		RequestJSON:       `{"options":[{"id":"allow","label":"Allow"},{"id":"cancel","label":"Cancel"}],"responder":"requester"}`,
		InteractionJSON:   `{"session_id":"tg-1-0","requested_by":{"user_id":"tg-101"},"locator":{"session_id":"tg-1-0","channel_type":"telegram","address_key":"1:0","address_json":"{\"chat_id\":1,\"topic_id\":0}"}}`,
		ResumeJSON:        `{"to":"session:tg-1-0"}`,
	}
}

func questionCallbackEvent(data string) *events.CallbackQueryEvent {
	message := client.MaybeInaccessibleMessage{
		"message_id": 42,
		"chat":       map[string]any{"id": int64(1), "type": "private"},
	}
	return &events.CallbackQueryEvent{CallbackQuery: &client.CallbackQuery{
		Id: "callback-1", Data: &data, From: client.User{Id: 101}, Message: &message,
	}}
}

func newQuestionServer(messenger TelegramMessenger) *Server {
	return &Server{
		channel: &Adapter{messenger: messenger, logger: zerolog.Nop()},
		logger:  zerolog.Nop(),
		now:     time.Now,
	}
}

func TestServerHandleQuestionReplyEnqueuesContinuationTurn(t *testing.T) {
	store := &fakeQuestionStore{
		record: baldastate.QuestionRecord{
			QuestionID:        "question-1",
			SessionID:         "tg-1-0",
			AddressKey:        "1:0",
			Provider:          "telegram",
			ConversationKey:   "1:0",
			ProviderMessageID: "42",
			Status:            questioncmd.StatusPending,
			RequestJSON:       `{"options":[{"id":"opt-1","label":"Allow once"},{"id":"opt-2","label":"Allow"},{"id":"opt-3","label":"Cancel"}]}`,
			InteractionJSON:   `{"session_id":"tg-1-0","channel_kind":"telegram","locator":{"session_id":"tg-1-0","channel_type":"telegram","address_key":"1:0","address_json":"{\"chat_id\":1,\"topic_id\":0}"}}`,
			ResumeJSON:        `{"to":"session:tg-1-0"}`,
		},
	}
	dispatcher := &fakeTurnDispatcher{}
	server := newQuestionServer(&callbackMessenger{})
	server.actorDispatcher = dispatcher
	server.questionService = questions.New(store, nil, zerolog.Nop())
	server.now = func() time.Time { return time.Date(2026, 7, 14, 6, 0, 0, 0, time.UTC) }

	handled, err := server.HandleQuestionReply(context.Background(), MessageContext{
		Locator:          baldasession.SessionLocator{SessionID: "tg-1-0", ChannelType: "telegram", AddressKey: "1:0", AddressJSON: `{"chat_id":1,"topic_id":0}`},
		TopicID:          0,
		MessageID:        43,
		ReplyToMessageID: 42,
		UserID:           101,
		Text:             "2",
	})
	if err != nil {
		t.Fatalf("HandleQuestionReply() error = %v", err)
	}
	if !handled {
		t.Fatal("HandleQuestionReply() handled = false, want true")
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
}

func TestServerHandleQuestionCallbackSettlesAndDispatchesContinuation(t *testing.T) {
	store := &fakeQuestionStore{record: callbackQuestionRecord(questioncmd.StatusPending)}
	dispatcher := &fakeTurnDispatcher{}
	messenger := &callbackMessenger{}
	ownerStore, collaboratorStore := questionCallbackAuthStores(t)
	server := newQuestionServer(messenger)
	server.ownerStore = ownerStore
	server.collaboratorStore = collaboratorStore
	server.actorDispatcher = dispatcher
	server.questionService = questions.New(store, nil, zerolog.Nop())
	server.now = func() time.Time { return time.Date(2026, 7, 15, 10, 0, 0, 0, time.UTC) }

	if err := server.HandleQuestionCallback(context.Background(), questionCallbackEvent("balda:q:question-1:2")); err != nil {
		t.Fatalf("HandleQuestionCallback() error = %v", err)
	}
	if len(messenger.answers) != 1 || messenger.answers[0] != questionCallbackSelectedMessage || messenger.alerts[0] {
		t.Fatalf("callback answers = %v alerts = %v", messenger.answers, messenger.alerts)
	}
	if len(dispatcher.commands) != 1 {
		t.Fatalf("dispatched commands = %d, want 1", len(dispatcher.commands))
	}
}
