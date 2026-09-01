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
	"strings"
	"testing"
	"time"

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
			if env.Inbound.ID != wantInboundID || env.Inbound.UserID != "slackagent:T123:U456" || env.Inbound.Text != "hello" {
				t.Fatalf("inbound = %+v", env.Inbound)
			}
		})
	}
}

func TestServerAcceptsSignedChannelMentionsAndKnownThreadCandidates(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		body                string
		wantConversationID  string
		wantRootTS          string
		wantExistingSession bool
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
			name:                "public channel reply requires known thread",
			body:                `{"type":"event_callback","event_id":"EvReply","team_id":"T123","event":{"type":"message","user":"U456","channel":"C789","channel_type":"channel","text":"follow up","ts":"1782234987.693923","thread_ts":"1782234671.392669"}}`,
			wantConversationID:  "C789",
			wantRootTS:          "1782234671.392669",
			wantExistingSession: true,
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
			if env.RequireExistingSession != test.wantExistingSession {
				t.Fatalf("RequireExistingSession = %v, want %v", env.RequireExistingSession, test.wantExistingSession)
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
