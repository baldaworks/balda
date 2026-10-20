package sessionapp

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	baldasession "github.com/baldaworks/balda/internal/apps/balda/session"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
)

type progressTargetStub struct {
	target baldasession.ProgressTarget
	err    error
}

func (s progressTargetStub) ResolveProgressTarget(context.Context, baldasession.SessionLocator) (baldasession.ProgressTarget, error) {
	return s.target, s.err
}

type progressDispatcherStub struct {
	envelopes []actorlayer.Envelope
	err       error
}

func (s *progressDispatcherStub) Dispatch(_ context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	s.envelopes = append(s.envelopes, env)
	return &actortransport.DispatchReceipt{}, s.err
}

type progressEventsStub struct {
	jobID     string
	eventType string
	actor     string
	messageID string
	payload   any
	calls     int
	err       error
}

func (s *progressEventsStub) AppendEvent(_ context.Context, jobID, eventType, actor, messageID string, payload any) error {
	s.calls++
	s.jobID, s.eventType, s.actor, s.messageID, s.payload = jobID, eventType, actor, messageID, payload
	return s.err
}

func TestProgressServiceRoutesConversationWithStableIdentity(t *testing.T) {
	t.Parallel()

	locator := testProgressLocator(t, "telegram", "10:42", `{"chat_id":10,"topic_id":42}`, "tg-10-42")
	dispatcher := &progressDispatcherStub{}
	service := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: locator, SessionID: locator.SessionID, Kind: baldasession.KindDM,
		}},
		dispatcher: dispatcher,
		logger:     zerolog.Nop(),
	}

	first, err := service.Report(t.Context(), locator, "working token=private-value", "step-1")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.Report(t.Context(), locator, "working token=private-value", "step-1")
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Disposition != progressDispositionDelivered || first.ProgressID == "" {
		t.Fatalf("results = %+v / %+v", first, second)
	}
	if len(dispatcher.envelopes) != 2 {
		t.Fatalf("dispatches = %d, want 2 attempts", len(dispatcher.envelopes))
	}
	for _, env := range dispatcher.envelopes {
		if env.ID != first.ProgressID || env.DedupeKey != first.ProgressID || env.To.Key != locator.DeliveryActorKey() {
			t.Fatalf("envelope identity = %+v", env)
		}
		var payload deliverycmd.Payload
		if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Locator != locator || payload.Settlement != deliverycmd.SettlementOutbox ||
			payload.Progress == nil || !payload.Progress.Visible ||
			strings.Contains(payload.Progress.Text, "private-value") {
			t.Fatalf("progress payload = %+v", payload)
		}
	}
}

func TestProgressServiceRecordsServiceWithoutDelivery(t *testing.T) {
	t.Parallel()

	locator := testProgressLocator(t, "webhook", "wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", `{}`,
		"wh-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	dispatcher := &progressDispatcherStub{}
	events := &progressEventsStub{}
	service := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: locator, SessionID: locator.SessionID, Kind: baldasession.KindWebhook, OwnerJobID: "webhook-job",
		}},
		dispatcher: dispatcher,
		jobEvents:  events,
		logger:     zerolog.Nop(),
	}

	result, err := service.Report(t.Context(), locator, "working", "step-1")
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != progressDispositionRecorded || result.ProgressID == "" {
		t.Fatalf("result = %+v", result)
	}
	if len(dispatcher.envelopes) != 0 {
		t.Fatalf("service progress dispatched %d deliveries", len(dispatcher.envelopes))
	}
	if events.calls != 1 || events.jobID != "webhook-job" || events.eventType != "agent.progress" ||
		events.messageID != result.ProgressID {
		t.Fatalf("event = %+v", events)
	}
}

func TestProgressServiceCoversChatAndScheduleKinds(t *testing.T) {
	t.Parallel()

	chat := testProgressLocator(t, "telegram", "20:0", `{"chat_id":20,"topic_id":0}`, "tg-20-0")
	chatDispatcher := &progressDispatcherStub{}
	chatService := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: chat, SessionID: chat.SessionID, Kind: baldasession.KindChat,
		}},
		dispatcher: chatDispatcher,
		logger:     zerolog.Nop(),
	}
	if result, err := chatService.Report(t.Context(), chat, "chat progress", "chat-step"); err != nil ||
		result.Disposition != progressDispositionDelivered || len(chatDispatcher.envelopes) != 1 {
		t.Fatalf("chat result=%+v deliveries=%d error=%v", result, len(chatDispatcher.envelopes), err)
	}

	schedule := testProgressLocator(t, "schedule", "arbitrary-service-session", `{"run_id":"arbitrary-service-session"}`, "arbitrary-service-session")
	scheduleDispatcher := &progressDispatcherStub{}
	scheduleEvents := &progressEventsStub{}
	scheduleService := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: schedule, SessionID: schedule.SessionID, Kind: baldasession.KindSchedule, OwnerJobID: "scheduled-job",
		}},
		dispatcher: scheduleDispatcher,
		jobEvents:  scheduleEvents,
		logger:     zerolog.Nop(),
	}
	if result, err := scheduleService.Report(t.Context(), schedule, "schedule progress", "schedule-step"); err != nil ||
		result.Disposition != progressDispositionRecorded || scheduleEvents.calls != 1 || len(scheduleDispatcher.envelopes) != 0 {
		t.Fatalf("schedule result=%+v events=%d deliveries=%d error=%v", result, scheduleEvents.calls, len(scheduleDispatcher.envelopes), err)
	}
}

func TestProgressServiceLogsNoTextOrAddress(t *testing.T) {
	t.Parallel()

	locator := testProgressLocator(t, "webhook", "wh-log-safe", `{"secret_route":"private"}`, "wh-log-safe")
	events := &progressEventsStub{}
	var logs bytes.Buffer
	service := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: locator, SessionID: locator.SessionID, Kind: baldasession.KindWebhook, OwnerJobID: "webhook-job",
		}},
		jobEvents: events,
		logger:    zerolog.New(&logs),
	}
	if _, err := service.Report(t.Context(), locator, "private progress text", "step-1"); err != nil {
		t.Fatal(err)
	}
	if got := logs.String(); strings.Contains(got, "private progress text") || strings.Contains(got, "secret_route") {
		t.Fatalf("unsafe progress log = %q", got)
	}
}

func TestProgressServiceReturnsBackendFailures(t *testing.T) {
	t.Parallel()

	locator := testProgressLocator(t, "telegram", "10:42", `{"chat_id":10,"topic_id":42}`, "tg-10-42")
	wantErr := errors.New("transport unavailable")
	service := &ProgressService{
		targets: progressTargetStub{target: baldasession.ProgressTarget{
			Locator: locator, SessionID: locator.SessionID, Kind: baldasession.KindChat,
		}},
		dispatcher: &progressDispatcherStub{err: wantErr},
		logger:     zerolog.Nop(),
	}
	if _, err := service.Report(t.Context(), locator, "working", "step-1"); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want backend failure", err)
	}
}

func testProgressLocator(t *testing.T, channelType, addressKey, addressJSON, sessionID string) baldasession.SessionLocator {
	t.Helper()
	locator, err := baldasession.NewSessionLocator(channelType, addressKey, addressJSON, sessionID)
	if err != nil {
		t.Fatal(err)
	}
	return locator
}
