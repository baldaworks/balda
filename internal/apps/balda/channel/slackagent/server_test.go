package slackagent

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/baldaworks/balda/internal/apps/balda/commandcmd"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/rs/zerolog"
)

func TestServerAcceptsSignedNativeMessageIMInCanonicalThread(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		ts         string
		threadTS   string
		wantRootTS string
	}{
		{name: "top level", ts: "1782234671.392669", wantRootTS: "1782234671.392669"},
		{name: "thread reply", ts: "1782234987.693923", threadTS: "1782234671.392669", wantRootTS: "1782234671.392669"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &recordingInboundProcessor{settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}}
			handler := newTestServer(processor, &recordingTurnCanceller{})
			body := []byte(fmt.Sprintf(`{"type":"event_callback","event_id":"Ev123","team_id":"T123","event":{"type":"message","user":"U456","channel":"D789","text":"hello","ts":%q,"thread_ts":%q,"channel_type":"im"}}`, test.ts, test.threadTS))
			rec := httptest.NewRecorder()

			handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", body))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if len(processor.events) != 1 {
				t.Fatalf("processed events = %d, want 1", len(processor.events))
			}
			env := processor.events[0]
			address, ok, err := DecodeLocator(env.Locator)
			if err != nil || !ok {
				t.Fatalf("DecodeLocator() = (%+v, %v, %v)", address, ok, err)
			}
			if address.TeamID != "T123" || address.ConversationID != "D789" || address.ThreadID != test.wantRootTS {
				t.Fatalf("address = %+v", address)
			}
			wantInboundID := turncmd.InboundID("slackagent:message:T123:D789:" + test.ts)
			if env.Chat.ID != wantInboundID || env.Chat.UserID != "slackagent:T123:U456" || env.Chat.Text != "hello" {
				t.Fatalf("inbound = %+v", env.Chat)
			}
		})
	}
}

func TestServerAcceptsSignedChannelMentions(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name               string
		body               string
		wantConversationID string
		wantRootTS         string
		wantContext        bool
	}{
		{
			name:               "public channel mention starts thread",
			body:               `{"type":"event_callback","event_id":"EvMention","team_id":"T123","event":{"type":"app_mention","user":"U456","channel":"C789","text":"<@UBOT> hello","ts":"1782234671.392669"}}`,
			wantConversationID: "C789",
			wantRootTS:         "1782234671.392669",
		},
		{
			name:               "private channel mention starts thread",
			body:               `{"type":"event_callback","event_id":"EvMention","team_id":"T123","event":{"type":"app_mention","user":"U456","channel":"G789","text":"<@UBOT> hello","ts":"1782234671.392669"}}`,
			wantConversationID: "G789",
			wantRootTS:         "1782234671.392669",
		},
		{
			name:               "public channel mention joins foreign thread",
			body:               `{"type":"event_callback","event_id":"EvReply","team_id":"T123","event":{"type":"app_mention","user":"U456","channel":"C789","channel_type":"channel","text":"<@UBOT> follow up","ts":"1782234987.693923","thread_ts":"1782234671.392669"}}`,
			wantConversationID: "C789",
			wantRootTS:         "1782234671.392669",
			wantContext:        true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &recordingInboundProcessor{settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundAccepted}}
			handler := newTestServer(processor, &recordingTurnCanceller{})
			rec := httptest.NewRecorder()

			handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", []byte(test.body)))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if len(processor.events) != 1 {
				t.Fatalf("processed events = %d, want 1", len(processor.events))
			}
			env := processor.events[0]
			address, ok, err := DecodeLocator(env.Locator)
			if err != nil || !ok {
				t.Fatalf("DecodeLocator() = (%+v, %v, %v)", address, ok, err)
			}
			if address.ConversationID != test.wantConversationID || address.ThreadID != test.wantRootTS {
				t.Fatalf("address = %+v", address)
			}
			if (env.ThreadContext != nil) != test.wantContext {
				t.Fatalf("ThreadContext = %+v, wantContext %v", env.ThreadContext, test.wantContext)
			}
		})
	}
}

func TestServerAcknowledgesIgnoredMessageEventsWithoutDispatch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		extra string
	}{
		{name: "subtype", extra: `,"subtype":"message_changed"`},
		{name: "bot id", extra: `,"bot_id":"B123"`},
		{name: "bot profile", extra: `,"bot_profile":{"id":"B123"}`},
		{name: "not im", extra: `,"channel_type":"channel"`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			processor := &recordingInboundProcessor{}
			handler := newTestServer(processor, &recordingTurnCanceller{})
			channelType := `,"channel_type":"im"`
			if strings.Contains(test.extra, "channel_type") {
				channelType = ""
			}
			body := []byte(`{"type":"event_callback","event_id":"Ev123","team_id":"T123","event":{"type":"message","user":"U456","channel":"D789","text":"hello","ts":"1782234671.392669"` + channelType + test.extra + `}}`)
			rec := httptest.NewRecorder()

			handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", body))

			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
			}
			if len(processor.events) != 0 {
				t.Fatalf("processed events = %d, want 0", len(processor.events))
			}
		})
	}
}

func TestServerHandlesSignedAgentSessionStopped(t *testing.T) {
	t.Parallel()
	canceller := &recordingTurnCanceller{}
	handler := newTestServer(&recordingInboundProcessor{}, canceller)
	body := []byte(`{"type":"event_callback","event_id":"EvStop","team_id":"T123","event":{"type":"agent_session_stopped","user":"U456","channel":"D789","thread_ts":"1782234671.392669","event_ts":"1783536983.783769","streaming_message_ts":["1782234987.693923"]}}`)
	rec := httptest.NewRecorder()

	handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", body))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if len(canceller.events) != 1 {
		t.Fatalf("cancellations = %d, want 1", len(canceller.events))
	}
	stopped := canceller.events[0]
	if stopped.RequestedBy != "slackagent:T123:U456" || len(stopped.StreamingMessageTS) != 1 || stopped.StreamingMessageTS[0] != "1782234987.693923" {
		t.Fatalf("stopped = %+v", stopped)
	}
	address, ok, err := DecodeLocator(stopped.Locator)
	if err != nil || !ok || address.ThreadID != "1782234671.392669" {
		t.Fatalf("DecodeLocator() = (%+v, %v, %v)", address, ok, err)
	}
}

func TestServerURLVerificationAndSignatureRejection(t *testing.T) {
	t.Parallel()
	processor := &recordingInboundProcessor{}
	handler := newTestServer(processor, &recordingTurnCanceller{})
	body := []byte(`{"type":"url_verification","challenge":"challenge-value"}`)
	rec := httptest.NewRecorder()
	handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", body))
	if rec.Code != http.StatusOK || rec.Body.String() != "challenge-value" {
		t.Fatalf("verification response = (%d, %q)", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "wrong-secret", body))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("invalid signature status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(processor.events) != 0 {
		t.Fatalf("processed events = %d, want 0", len(processor.events))
	}

	rec = httptest.NewRecorder()
	staleAt := time.Now().Add(-6 * time.Minute)
	handler.handleEvents(rec, signedSlackRequestAt(t, "/slack/agent/events", "secret", body, staleAt))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale signature status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(processor.events) != 0 {
		t.Fatalf("processed events after stale request = %d, want 0", len(processor.events))
	}
}

func TestServerRoutesSignedSlashCommandsToCommonHandler(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		path string
		text string
	}{
		{name: "default route", path: "/slack/commands", text: "locator"},
		{name: "custom route preserves command and args", path: "/custom/commands", text: "reset alpha beta"},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingCommandHandler{}
			processor := &recordingInboundProcessor{}
			canceller := &recordingTurnCanceller{}
			server := newTestServer(processor, canceller)
			server.commandHandler = recorder
			if test.path != "/slack/commands" {
				server.config.CommandsPath = test.path
			}
			handler, _, _, err := server.httpHandler()
			if err != nil {
				t.Fatalf("httpHandler() error = %v", err)
			}
			body := url.Values{
				"command":    {"/balda"},
				"text":       {test.text},
				"team_id":    {"T123"},
				"channel_id": {"C456"},
				"user_id":    {"U789"},
			}.Encode()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, signedSlackRequest(t, test.path, "secret", []byte(body)))

			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d", response.Code, http.StatusOK)
			}
			if len(recorder.requests) != 1 {
				t.Fatalf("common handler calls = %d, want 1", len(recorder.requests))
			}
			if len(processor.events) != 0 || len(canceller.events) != 0 {
				t.Fatalf("slash command reached chat lifecycle: processor=%d canceller=%d", len(processor.events), len(canceller.events))
			}
			request := recorder.requests[0]
			wantCommand := strings.Fields(test.text)[0]
			if request.Payload.Name != wantCommand || request.Payload.Locator.AddressKey != "c:T123:C456" {
				t.Fatalf("command request = %+v", request)
			}
			if request.Payload.Principal != "slackagent:T123:U789" || !request.Payload.Access.SessionCommands || request.Payload.Invocation.Root != "/balda" {
				t.Fatalf("command identity/access/invocation = %+v", request)
			}
			if test.name == "custom route preserves command and args" && request.Payload.Args != "alpha beta" {
				t.Fatalf("args = %q, want alpha beta", request.Payload.Args)
			}
		})
	}
}

func TestServerReturnsUsageForUnsupportedSlashCommands(t *testing.T) {
	t.Parallel()
	for _, commandText := range []string{"", "unknown"} {
		t.Run(commandText, func(t *testing.T) {
			recorder := &recordingCommandHandler{}
			server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
			server.commandHandler = recorder
			handler, _, _, err := server.httpHandler()
			if err != nil {
				t.Fatalf("httpHandler() error = %v", err)
			}
			body := url.Values{"command": {"/balda"}, "text": {commandText}, "team_id": {"T123"}, "channel_id": {"C456"}, "user_id": {"U789"}}.Encode()
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, signedSlackRequest(t, "/slack/commands", "secret", []byte(body)))
			if response.Code != http.StatusOK || strings.TrimSpace(response.Body.String()) != "Usage: /balda locator | reset" {
				t.Fatalf("response status=%d body=%q", response.Code, response.Body.String())
			}
			if len(recorder.requests) != 0 {
				t.Fatalf("common handler calls = %d, want 0", len(recorder.requests))
			}
		})
	}
}

func TestServerRejectsInvalidSlashCommandsBeforeCommonHandler(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		body       string
		secret     string
		wantStatus int
	}{
		{name: "unexpected root command", body: "command=%2Fother&text=locator&team_id=T123&channel_id=C456&user_id=U789", secret: "secret", wantStatus: http.StatusBadRequest},
		{name: "missing team", body: "command=%2Fbalda&text=locator&channel_id=C456&user_id=U789", secret: "secret", wantStatus: http.StatusBadRequest},
		{name: "unknown conversation", body: "command=%2Fbalda&text=locator&team_id=T123&channel_id=X456&user_id=U789", secret: "secret", wantStatus: http.StatusBadRequest},
		{name: "invalid signature", body: "command=%2Fbalda&text=locator&team_id=T123&channel_id=C456&user_id=U789", secret: "wrong", wantStatus: http.StatusUnauthorized},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingCommandHandler{}
			server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
			server.commandHandler = recorder
			handler, _, _, err := server.httpHandler()
			if err != nil {
				t.Fatalf("httpHandler() error = %v", err)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, signedSlackRequest(t, "/slack/commands", test.secret, []byte(test.body)))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if len(recorder.requests) != 0 {
				t.Fatalf("common handler calls = %d, want 0", len(recorder.requests))
			}
		})
	}
}

func TestServerAppliesHTTPBoundariesToSlashCommands(t *testing.T) {
	t.Parallel()
	body := []byte("command=%2Fbalda&text=locator&team_id=T123&channel_id=C456&user_id=U789")
	for _, test := range []struct {
		name       string
		request    func(*testing.T) *http.Request
		wantStatus int
	}{
		{
			name: "method",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				request := signedSlackRequest(t, "/slack/commands", "secret", body)
				request.Method = http.MethodGet
				return request
			},
			wantStatus: http.StatusMethodNotAllowed,
		},
		{
			name: "stale signature",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return signedSlackRequestAt(t, "/slack/commands", "secret", body, time.Now().Add(-6*time.Minute))
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "oversized body",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return signedSlackRequest(t, "/slack/commands", "secret", []byte(strings.Repeat("x", webhookMaxBodyBytes+1)))
			},
			wantStatus: http.StatusRequestEntityTooLarge,
		},
		{
			name: "malformed form",
			request: func(t *testing.T) *http.Request {
				t.Helper()
				return signedSlackRequest(t, "/slack/commands", "secret", []byte("command=%zz"))
			},
			wantStatus: http.StatusBadRequest,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			recorder := &recordingCommandHandler{}
			server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
			server.commandHandler = recorder
			response := httptest.NewRecorder()
			server.handleCommands(response, test.request(t))
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
			if len(recorder.requests) != 0 {
				t.Fatalf("common handler calls = %d, want 0", len(recorder.requests))
			}
		})
	}
}

func TestServerReturnsRetryWhenCommonCommandHandlerFails(t *testing.T) {
	t.Parallel()
	recorder := &recordingCommandHandler{err: errors.New("dispatch unavailable")}
	server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
	server.commandHandler = recorder
	body := []byte("command=%2Fbalda&text=locator&team_id=T123&channel_id=C456&user_id=U789")
	response := httptest.NewRecorder()
	server.handleCommands(response, signedSlackRequest(t, "/slack/commands", "secret", body))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusServiceUnavailable)
	}
}

func TestServerRejectsCollidingEffectiveRoutes(t *testing.T) {
	t.Parallel()
	server := newTestServer(&recordingInboundProcessor{}, &recordingTurnCanceller{})
	server.config.EventsPath = "/same"
	server.config.CommandsPath = "/same"
	if _, _, _, err := server.httpHandler(); err == nil || !strings.Contains(err.Error(), "must differ") {
		t.Fatalf("httpHandler() error = %v, want path collision", err)
	}
}

func TestServerReturnsRetrySettlement(t *testing.T) {
	t.Parallel()
	processor := &recordingInboundProcessor{
		settlement: turncmd.InboundSettlement{Outcome: turncmd.InboundRetry},
		err:        errors.New("temporary ingress failure"),
	}
	handler := newTestServer(processor, &recordingTurnCanceller{})
	body := []byte(`{"type":"event_callback","event_id":"Ev123","team_id":"T123","event":{"type":"message","user":"U456","channel":"D789","text":"hello","ts":"1782234671.392669","channel_type":"im"}}`)
	rec := httptest.NewRecorder()

	handler.handleEvents(rec, signedSlackRequest(t, "/slack/agent/events", "secret", body))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func newTestServer(processor InboundProcessor, canceller TurnCanceller) *Server {
	return &Server{
		inboundProcessor: processor,
		turnCanceller:    canceller,
		config:           Config{SigningSecret: "secret"},
		logger:           zerolog.Nop(),
		processSem:       make(chan struct{}, 1),
	}
}

type recordingInboundProcessor struct {
	events     []IngressEnvelope
	settlement turncmd.InboundSettlement
	err        error
}

func (p *recordingInboundProcessor) ProcessInbound(_ context.Context, env IngressEnvelope) (turncmd.InboundSettlement, error) {
	p.events = append(p.events, env)
	return p.settlement, p.err
}

type recordingTurnCanceller struct {
	events []SessionStopped
	err    error
}

type recordingCommandHandler struct {
	requests []commandcmd.Request
	err      error
}

func (h *recordingCommandHandler) PublishCommand(_ context.Context, request commandcmd.Request) error {
	h.requests = append(h.requests, request)
	return h.err
}

func (c *recordingTurnCanceller) CancelTurn(_ context.Context, stopped SessionStopped) error {
	c.events = append(c.events, stopped)
	return c.err
}

func signedSlackRequest(t *testing.T, path, secret string, body []byte) *http.Request {
	t.Helper()
	return signedSlackRequestAt(t, path, secret, body, time.Now())
}

func signedSlackRequestAt(t *testing.T, path, secret string, body []byte, at time.Time) *http.Request {
	t.Helper()
	timestamp := fmt.Sprintf("%d", at.Unix())
	base := signatureVersion + ":" + timestamp + ":" + string(body)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(base))
	signature := signatureVersion + "=" + hex.EncodeToString(mac.Sum(nil))
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(body)))
	req.Header.Set("X-Slack-Request-Timestamp", timestamp)
	req.Header.Set("X-Slack-Signature", signature)
	return req
}
