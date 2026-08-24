package slackagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"
	"unsafe"

	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestServerProcessEventPublishesDirectSessionTurn(t *testing.T) {
	locator := NewConversationLocator("T123", "C456")
	ts := newTopicSession(t, locator.SessionID)
	setUnexportedField(t, ts, "userID", "slack:T123:U456")
	setUnexportedField(t, ts, "agentSessionID", "agent-session-1")
	sessionManager := newSessionManagerWithSession(t, locator, ts)
	dispatcher := &recordingCommandBus{}
	handler := &Server{
		sessionManager:  sessionManager,
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
	}

	settlement, err := handler.processEvent(context.Background(), BuildIngressEnvelope(EventEnvelope{
		Event: Event{
			EventID:      "evt-123",
			UserID:       "U456",
			Text:         "hello from slack agent",
			Conversation: ConversationRef{TeamID: "T123", ConversationID: "C456"},
			Message:      &MessageRef{MessageID: "msg-123"},
		},
	}, testTime()))
	if err != nil || settlement.Outcome != turncmd.InboundAccepted {
		t.Fatalf("processEvent() settlement = %+v, error = %v", settlement, err)
	}

	var envFound bool
	var envPayload actors.SessionTurnPayload
	for _, env := range dispatcher.commands {
		if env.To.Target != baldaexecution.ActorTypeSession {
			continue
		}
		if got, want := env.DedupeKey, "slack_agent:evt-123"; got != want {
			t.Fatalf("dedupe_key = %q, want %q", got, want)
		}
		if err := actorlayer.UnmarshalPayload(env.Payload, &envPayload); err != nil {
			t.Fatalf("decode session turn payload: %v", err)
		}
		envFound = true
		break
	}
	if !envFound {
		t.Fatalf("session command not found in published commands: %+v", dispatcher.commands)
	}
	if envPayload.Source != "slack_agent" || !envPayload.Deliver {
		t.Fatalf("session turn payload = %+v, want slack_agent deliver=true", envPayload)
	}
	if got, want := envPayload.DedupeKey, "slack_agent:evt-123"; got != want {
		t.Fatalf("payload dedupe_key = %q, want %q", got, want)
	}
	if got, want := envPayload.UserID, "slack:T123:U456"; got != want {
		t.Fatalf("payload user_id = %q, want %q", got, want)
	}
}

func TestServerHTTPSettlementWaitsForDurableAcceptance(t *testing.T) {
	locator := NewConversationLocator("T123", "C456")
	ts := newTopicSession(t, locator.SessionID)
	setUnexportedField(t, ts, "userID", "slack:T123:U456")
	sessionManager := newSessionManagerWithSession(t, locator, ts)
	body := []byte(`{"type":"event_callback","event_id":"evt-123","team_id":"T123","event":{"type":"message","user_id":"U456","text":"hello","conversation_id":"C456","message_id":"msg-123"}}`)

	for _, test := range []struct {
		name        string
		dispatchErr error
		wantStatus  int
	}{
		{name: "accepted", wantStatus: http.StatusOK},
		{name: "retry", dispatchErr: actorlayer.TransientError(errors.New("temporary dispatch failure")), wantStatus: http.StatusServiceUnavailable},
	} {
		t.Run(test.name, func(t *testing.T) {
			dispatcher := &recordingCommandBus{}
			if test.dispatchErr != nil {
				dispatcher.commandErrs = []error{test.dispatchErr}
			}
			handler := &Server{
				sessionManager:  sessionManager,
				actorDispatcher: dispatcher,
				config:          Config{SigningSecret: "secret"},
				logger:          zerolog.Nop(),
				processSem:      make(chan struct{}, 1),
			}
			req := signedSlackRequest(t, "/slack/agent/events", "secret", body)
			rec := httptest.NewRecorder()

			handler.handleEvents(rec, req)

			if rec.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, test.wantStatus)
			}
		})
	}
}

func TestServerHandleQuestionReplyEnqueuesContinuation(t *testing.T) {
	store := &fakeQuestionStore{
		record: baldastate.QuestionRecord{
			QuestionID:        "question-1",
			SessionID:         "sla-1",
			AddressKey:        "c:T123:C456",
			Provider:          "slack_agent",
			ConversationKey:   "c:T123:C456",
			ProviderMessageID: "reply-target-1",
			Status:            questioncmd.StatusPending,
			InteractionJSON:   `{"session_id":"sla-1","channel_kind":"slack_agent","locator":{"session_id":"sla-1","channel_type":"slack_agent","address_key":"c:T123:C456","address_json":"{\"team_id\":\"T123\",\"conversation_id\":\"C456\"}"}}`,
			ResumeJSON:        `{"to":"session:sla-1"}`,
		},
	}
	dispatcher := &recordingCommandBus{}
	handler := &Server{
		actorDispatcher: dispatcher,
		questionService: questions.New(store, nil, zerolog.Nop()),
		logger:          zerolog.Nop(),
	}
	handled, err := handler.handleQuestionReply(context.Background(), BuildIngressEnvelope(EventEnvelope{
		Event: Event{
			UserID: "U456",
			Text:   "answer",
			Conversation: ConversationRef{
				TeamID:         "T123",
				ConversationID: "C456",
			},
			Message: &MessageRef{
				Conversation: ConversationRef{TeamID: "T123", ConversationID: "C456"},
				MessageID:    "message-2",
				ThreadTS:     "reply-target-1",
			},
		},
	}, testTime()))
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
	if payload.Answer.Text != "answer" {
		t.Fatalf("answer text = %q, want answer", payload.Answer.Text)
	}
}

func signedSlackRequest(t *testing.T, path, secret string, body []byte) *http.Request {
	t.Helper()
	timestamp := fmt.Sprintf("%d", time.Now().Unix())
	base := signatureVersion + ":" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	signature := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)
	return req
}

func testTime() time.Time {
	return time.Date(2026, time.August, 4, 10, 0, 0, 0, time.UTC)
}

type recordingCommandBus struct {
	commands    []actorlayer.Envelope
	commandErrs []error
}

func (b *recordingCommandBus) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	b.commands = append(b.commands, env)
	var err error
	if len(b.commandErrs) > 0 {
		err = b.commandErrs[0]
		b.commandErrs = b.commandErrs[1:]
	}
	return &actortransport.DispatchReceipt{}, err
}

func (b *recordingCommandBus) PublishEvent(_ context.Context, _ string, _ actorlayer.Envelope) error {
	return nil
}

func newSessionManagerWithSession(t *testing.T, locator baldasession.SessionLocator, ts *baldasession.TopicSession) *baldasession.Manager {
	t.Helper()
	m := &baldasession.Manager{}
	setUnexportedField(t, m, "sessions", map[string]*baldasession.TopicSession{locator.SessionID: ts})
	setUnexportedField(t, m, "sessionStore", &fakeRestoreSessionStore{})
	return m
}

func newTopicSession(t *testing.T, sessionID string) *baldasession.TopicSession {
	t.Helper()
	ts := &baldasession.TopicSession{}
	setUnexportedField(t, ts, "sessionID", sessionID)
	return ts
}

func setUnexportedField[T any](t *testing.T, target any, fieldName string, value T) {
	t.Helper()
	rv := reflect.ValueOf(target).Elem().FieldByName(fieldName)
	reflect.NewAt(rv.Type(), unsafe.Pointer(rv.UnsafeAddr())).Elem().Set(reflect.ValueOf(value))
}

type fakeRestoreSessionStore struct{}

func (fakeRestoreSessionStore) Upsert(context.Context, baldastate.SessionRecord) error {
	return nil
}

func (fakeRestoreSessionStore) GetByAddress(context.Context, string, string) (baldastate.SessionRecord, bool, error) {
	return baldastate.SessionRecord{}, false, nil
}

func (fakeRestoreSessionStore) GetBySessionID(context.Context, string) (baldastate.SessionRecord, bool, error) {
	return baldastate.SessionRecord{}, false, nil
}

func (fakeRestoreSessionStore) DeleteBySessionID(context.Context, string) error {
	return nil
}

func (fakeRestoreSessionStore) List(context.Context) ([]baldastate.SessionRecord, error) {
	return nil, nil
}

type fakeQuestionStore struct {
	record baldastate.QuestionRecord
}

func (s *fakeQuestionStore) CreatePendingQuestion(context.Context, baldastate.QuestionRecord) error {
	return nil
}

func (s *fakeQuestionStore) BindQuestionDeliveryRef(context.Context, string, questioncmd.DeliveryRef) error {
	return nil
}

func (s *fakeQuestionStore) GetQuestionByID(context.Context, string) (baldastate.QuestionRecord, bool, error) {
	return s.record, true, nil
}

func (s *fakeQuestionStore) GetPendingQuestionByReplyRef(_ context.Context, provider, conversationKey, replyToMessageID string) (baldastate.QuestionRecord, bool, error) {
	if provider == s.record.Provider && conversationKey == s.record.AddressKey && replyToMessageID == s.record.ProviderMessageID {
		return s.record, true, nil
	}
	return baldastate.QuestionRecord{}, false, nil
}

func (s *fakeQuestionStore) MarkQuestionAnswered(_ context.Context, questionID string, answer questioncmd.Answer) (baldastate.QuestionRecord, bool, error) {
	if questionID != s.record.QuestionID {
		return baldastate.QuestionRecord{}, false, nil
	}
	s.record.Status = questioncmd.StatusAnswered
	encoded, err := json.Marshal(answer)
	if err != nil {
		return baldastate.QuestionRecord{}, false, err
	}
	s.record.AnswerJSON = string(encoded)
	return s.record, true, nil
}

func (s *fakeQuestionStore) MarkQuestionTimedOut(context.Context, string, time.Time) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}

func (s *fakeQuestionStore) MarkQuestionFailed(context.Context, string, questioncmd.Failure) (baldastate.QuestionRecord, bool, error) {
	return baldastate.QuestionRecord{}, false, nil
}
