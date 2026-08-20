package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/baldaworks/go-actorlayer"
	"github.com/baldaworks/balda/internal/apps/balda/actors"
	baldaslackagent "github.com/baldaworks/balda/internal/apps/balda/channel/slackagent"
	baldaexecution "github.com/baldaworks/balda/internal/apps/balda/execution"
	"github.com/baldaworks/balda/internal/apps/balda/questioncmd"
	"github.com/baldaworks/balda/internal/apps/balda/questions"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestSlackAgentHandlerProcessEventPublishesDirectSessionTurn(t *testing.T) {
	locator := baldaslackagent.NewConversationLocator("T123", "C456")
	ts := newBaldaTopicSession(t, locator.SessionID)
	setUnexportedField(t, ts, "userID", "slack:T123:U456")
	setUnexportedField(t, ts, "agentSessionID", "agent-session-1")
	sessionManager := newBaldaSessionManagerWithSession(t, locator, ts)
	dispatcher := &recordingHandlerCommandBus{}
	handler := &SlackAgentHandler{
		sessionManager:  sessionManager,
		actorDispatcher: dispatcher,
		logger:          zerolog.Nop(),
	}

	settlement, err := handler.processEvent(context.Background(), slackAgentEnvelope{
		EventID: "evt-123",
		TeamID:  "T123",
		Event: slackAgentEvent{
			UserID:         "U456",
			Text:           "hello from slack agent",
			ConversationID: "C456",
			MessageID:      "msg-123",
		},
	})
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

func TestSlackAgentHandlerHTTPSettlementWaitsForDurableAcceptance(t *testing.T) {
	locator := baldaslackagent.NewConversationLocator("T123", "C456")
	ts := newBaldaTopicSession(t, locator.SessionID)
	setUnexportedField(t, ts, "userID", "slack:T123:U456")
	sessionManager := newBaldaSessionManagerWithSession(t, locator, ts)
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
			dispatcher := &recordingHandlerCommandBus{}
			if test.dispatchErr != nil {
				dispatcher.commandErrs = []error{test.dispatchErr}
			}
			handler := &SlackAgentHandler{
				sessionManager:  sessionManager,
				actorDispatcher: dispatcher,
				config:          SlackAgentConfig{SigningSecret: "secret"},
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

func TestSlackAgentHandlerHandleQuestionReplyEnqueuesContinuation(t *testing.T) {
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
	dispatcher := &recordingHandlerCommandBus{}
	handler := &SlackAgentHandler{
		actorDispatcher: dispatcher,
		questionService: questions.New(store, nil, zerolog.Nop()),
		logger:          zerolog.Nop(),
	}
	locator := baldaslackagent.NewConversationLocator("T123", "C456")

	handled, err := handler.handleQuestionReply(context.Background(), locator, "slack:T123:U456", slackAgentEvent{
		Text:             "answer",
		MessageID:        "message-2",
		ReplyToMessageID: "reply-target-1",
	})
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
