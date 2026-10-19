package actors

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"path/filepath"
	"strings"
	"testing"

	"github.com/baldaworks/balda/internal/apps/balda/deliverycmd"
	"github.com/baldaworks/balda/internal/apps/balda/deliveryworkflow"
	baldastate "github.com/baldaworks/balda/internal/apps/balda/state"
	"github.com/baldaworks/balda/internal/apps/balda/turncmd"
	"github.com/baldaworks/go-actorlayer"
	actortransport "github.com/baldaworks/go-actorlayer/transport"
	"github.com/rs/zerolog"
	adkagent "google.golang.org/adk/v2/agent"
	adksession "google.golang.org/adk/v2/session"
	"google.golang.org/genai"
)

var errTerminalIterator = errors.New("session model is not supported credential=SYNTHETIC_SECRET")

func terminalIterator(ctx adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
	return func(yield func(*adksession.Event, error) bool) {
		event := adksession.NewEvent(ctx, ctx.InvocationID())
		event.Partial = true
		event.Content = genai.NewContentFromText("working", genai.RoleModel)
		if yield(event, nil) {
			yield(nil, errTerminalIterator)
		}
	}
}

func TestTerminalFailureNoticeConcreteDeliveryDistinctMessages(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.db")
	provider := openTerminalDeliveryStore(t, path)
	transport := &terminalNoticeTransport{}
	deliveryActor := newTerminalDeliveryActor(provider, transport)
	actor, bus, payload := executionFailureActor(t, terminalIterator)
	noticeIDs := map[string]bool{}
	for _, messageID := range []int{5316, 5317, 5318, 6001} {
		payload.MessageID = messageID
		if messageID == 6001 {
			payload.Locator = deliverycmd.Locator{SessionID: "tg-987654-42", ChannelType: "telegram", AddressKey: "987654:42", AddressJSON: `{"chat_id":987654,"topic_id":42}`}
		}
		env, err := turncmd.SessionTurnEnvelope(payload)
		if err != nil {
			t.Fatal(err)
		}
		env.ID = fmt.Sprintf("synthetic-telegram:%s:%d", payload.Locator.AddressKey, messageID)
		env.CorrelationID = fmt.Sprintf("synthetic-update-%d", messageID)
		env.MaxAttempts = 5
		before := len(terminalNotices(t, bus))
		for attempt := range 5 {
			env.Attempt = attempt
			if err := actor.Handle(t.Context(), env); !errors.Is(err, errTerminalIterator) {
				t.Fatalf("inbound=%s attempt=%d error=%v", env.ID, attempt+1, err)
			}
			count := len(terminalNotices(t, bus)) - before
			want := 0
			if attempt == 4 {
				want = 1
			}
			if count != want {
				t.Fatalf("inbound=%s attempt=%d notices=%d,want%d", env.ID, attempt+1, count, want)
			}
		}
		notices := terminalNotices(t, bus)
		notice := notices[len(notices)-1]
		if noticeIDs[notice.ID] {
			t.Fatalf("distinct inbound=%s reused notice=%s", env.ID, notice.ID)
		}
		noticeIDs[notice.ID] = true
		assertTerminalNotice(t, env, notice, payload.Locator)
		for range 2 {
			if err := deliveryActor.Handle(t.Context(), notice); err != nil {
				t.Fatal(err)
			}
		}
		receipt := fmt.Sprintf("fake-telegram-message-%d", len(transport.deliveries))
		assertTerminalReceipt(t, path, notice, payload.Locator, baldastate.DeliveryStatusSent, receipt)
		got := transport.deliveries[len(transport.deliveries)-1].Payload
		if got.Locator != payload.Locator || got.Text != terminalFailureMessage || strings.Contains(got.Text, "SYNTHETIC_SECRET") {
			t.Fatalf("unsafe/misdirected concrete send=%+v", got)
		}
		t.Logf("inbound=%s attempt=5/5 terminal=failure notice=%s locator=%s:%s concrete_receipt=%s", env.ID, notice.ID, got.Locator.ChannelType, got.Locator.AddressKey, receipt)
	}
	if len(transport.deliveries) != 4 {
		t.Fatalf("concrete calls=%d,want4 distinct notices", len(transport.deliveries))
	}
}

func TestTerminalFailureNoticeReplayAcrossSQLiteRestart(t *testing.T) {
	for _, beforeConsumption := range []bool{true, false} {
		t.Run(fmt.Sprintf("before-consumption-%t", beforeConsumption), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "delivery.db")
			provider := openTerminalDeliveryStore(t, path)
			transport := &terminalNoticeTransport{}
			actor, bus, payload := executionFailureActor(t, terminalIterator)
			env, err := turncmd.SessionTurnEnvelope(payload)
			if err != nil {
				t.Fatal(err)
			}
			env.ID, env.CorrelationID = "synthetic-inbound-5316", "synthetic-update-5316"
			env.Attempt, env.MaxAttempts = 4, 5
			for range 2 {
				if err := actor.Handle(t.Context(), env); !errors.Is(err, errTerminalIterator) {
					t.Fatal(err)
				}
			}
			notices := terminalNotices(t, bus)
			if len(notices) != 2 || notices[0].ID != notices[1].ID || notices[0].Payload.String() != notices[1].Payload.String() {
				t.Fatal("repeated terminal handling changed notice identity or payload")
			}
			notice := notices[0]
			assertTerminalNotice(t, env, notice, payload.Locator)
			if !beforeConsumption {
				if err := newTerminalDeliveryActor(provider, transport).Handle(t.Context(), notice); err != nil {
					t.Fatal(err)
				}
				assertTerminalReceipt(t, path, notice, payload.Locator, baldastate.DeliveryStatusSent, "fake-telegram-message-1")
			}
			if err := provider.Close(); err != nil {
				t.Fatal(err)
			}
			provider = openTerminalDeliveryStore(t, path)
			// Reconstruct both product actors and replay the retained inbound/accepted
			// delivery envelopes, as durable transport redelivery does after restart.
			restarted, busAfter, _ := executionFailureActor(t, terminalIterator)
			if err := restarted.Handle(t.Context(), env); !errors.Is(err, errTerminalIterator) {
				t.Fatal(err)
			}
			replay := terminalNotices(t, busAfter)
			if len(replay) != 1 || replay[0].ID != notice.ID || replay[0].Payload.String() != notice.Payload.String() {
				t.Fatal("restart changed accepted notice identity/payload")
			}
			deliveryActor := newTerminalDeliveryActor(provider, transport)
			for _, command := range []actorlayer.Envelope{notice, notices[1], replay[0], notice} {
				if err := deliveryActor.Handle(t.Context(), command); err != nil {
					t.Fatal(err)
				}
			}
			if len(transport.deliveries) != 1 {
				t.Fatalf("concrete calls after restart/replay=%d,want1", len(transport.deliveries))
			}
			assertTerminalReceipt(t, path, notice, payload.Locator, baldastate.DeliveryStatusSent, "fake-telegram-message-1")
			t.Logf("inbound=%s notice=%s locator=telegram:%s restart_before_consumption=%t confirmed_receipt=fake-telegram-message-1 concrete_calls=1", env.ID, notice.ID, payload.Locator.AddressKey, beforeConsumption)
		})
	}
}

func TestTerminalFailureNoticeAmbiguousSendRetainsSendingAcrossRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.db")
	provider := openTerminalDeliveryStore(t, path)
	actor, bus, payload := executionFailureActor(t, terminalIterator)
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.ID = "synthetic-inbound-ambiguous-5317"
	env.Attempt, env.MaxAttempts = 4, 5
	if err := actor.Handle(t.Context(), env); !errors.Is(err, errTerminalIterator) {
		t.Fatal(err)
	}
	notices := terminalNotices(t, bus)
	if len(notices) != 1 {
		t.Fatalf("notices=%d,want1", len(notices))
	}
	notice := notices[0]
	transport := &terminalNoticeTransport{err: deliverycmd.AmbiguousError(errors.New("provider accepted but receipt was lost"))}
	if err := newTerminalDeliveryActor(provider, transport).Handle(t.Context(), notice); err == nil {
		t.Fatal("ambiguous provider result returned delivery success")
	}
	assertTerminalReceipt(t, path, notice, payload.Locator, baldastate.DeliveryStatusSending, "")
	if err := provider.Close(); err != nil {
		t.Fatal(err)
	}
	provider = openTerminalDeliveryStore(t, path)
	transport.err = nil
	deliveryActor := newTerminalDeliveryActor(provider, transport)
	for range 2 {
		if err := deliveryActor.Handle(t.Context(), notice); err == nil {
			t.Fatal("ambiguous replay returned success")
		}
	}
	if len(transport.deliveries) != 1 {
		t.Fatalf("ambiguous replay concrete calls=%d,want1", len(transport.deliveries))
	}
	assertTerminalReceipt(t, path, notice, payload.Locator, baldastate.DeliveryStatusSending, "")
	t.Logf("inbound=%s notice=%s locator=telegram:%s outcome=ambiguous durable_status=sending concrete_calls=1 receipt=unconfirmed", env.ID, notice.ID, payload.Locator.AddressKey)
}

func TestTerminalFailureNoticePublicationFailureDoesNotReachConcreteTransport(t *testing.T) {
	path := filepath.Join(t.TempDir(), "delivery.db")
	provider := openTerminalDeliveryStore(t, path)
	transport := &terminalNoticeTransport{}
	deliveryActor := newTerminalDeliveryActor(provider, transport)
	actor, bus, payload := executionFailureActor(t, terminalIterator)
	publishErr := errors.New("notice publication unavailable")
	publication := &terminalNoticePublicationFailure{bus: bus, err: publishErr}
	actor.dispatcher = publication
	env, err := turncmd.SessionTurnEnvelope(payload)
	if err != nil {
		t.Fatal(err)
	}
	env.ID = "synthetic-inbound-publication-failure-5318"
	env.Attempt, env.MaxAttempts = 4, 5
	got := actor.Handle(t.Context(), env)
	if !errors.Is(got, publishErr) || !actorlayer.IsRetryableError(got) || !actorlayer.RetryExhausted(env.Attempt+1, env.MaxAttempts) {
		t.Fatalf("inbound=%s final-attempt publication outcome=%v", env.ID, got)
	}
	// Consume only commands actually accepted by the publishing boundary.
	for _, notice := range terminalNotices(t, bus) {
		if err := deliveryActor.Handle(t.Context(), notice); err != nil {
			t.Fatal(err)
		}
	}
	if len(terminalNotices(t, bus)) != 0 || len(transport.deliveries) != 0 {
		t.Fatalf("failed publication accepted_notices=%d concrete_calls=%d,want0/0", len(terminalNotices(t, bus)), len(transport.deliveries))
	}
	assertTerminalNotice(t, env, publication.attempted, payload.Locator)
	t.Logf("inbound=%s attempt=5/5 notice=%s locator=telegram:%s outcome=publication_failed bounded_policy=exhausted accepted_notices=0 concrete_calls=0", env.ID, publication.attempted.ID, payload.Locator.AddressKey)
}

func terminalNotices(t *testing.T, bus *recordingHandlerCommandBus) []actorlayer.Envelope {
	t.Helper()
	var notices []actorlayer.Envelope
	for _, command := range bus.commands {
		var payload deliverycmd.Payload
		if err := actorlayer.UnmarshalPayload(command.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if payload.Settlement == deliverycmd.SettlementOutbox && payload.Text == terminalFailureMessage {
			notices = append(notices, command)
		}
	}
	return notices
}

func assertTerminalNotice(t *testing.T, inbound, notice actorlayer.Envelope, locator deliverycmd.Locator) {
	t.Helper()
	var payload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(notice.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	correlation := inbound.CorrelationID
	if correlation == "" {
		correlation = inbound.ID
	}
	if notice.ID == "" || notice.ID != notice.DedupeKey || notice.CausationID != inbound.ID || notice.CorrelationID != correlation || payload.Locator != locator || payload.Text != terminalFailureMessage || payload.JobID != "" || payload.Settlement != deliverycmd.SettlementOutbox {
		t.Fatalf("inbound=%s notice=%+v payload=%+v", inbound.ID, notice, payload)
	}
}

func openTerminalDeliveryStore(t *testing.T, path string) baldastate.Provider {
	t.Helper()
	provider, err := baldastate.NewSQLiteProvider(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = provider.Close() })
	return provider
}
func newTerminalDeliveryActor(provider baldastate.Provider, transport *terminalNoticeTransport) *JobDeliveryActor {
	return NewJobDeliveryActor(deliveryworkflow.New(transport, provider.Jobs(), nil, nil, nil, zerolog.Nop()))
}

func assertTerminalReceipt(t *testing.T, path string, notice actorlayer.Envelope, locator deliverycmd.Locator, status, receipt string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Close(); err != nil {
			t.Errorf("close delivery receipt reader: %v", err)
		}
	}()
	var jobs int
	if err := db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM execution_jobs`).Scan(&jobs); err != nil {
		t.Fatal(err)
	}
	if jobs != 0 {
		t.Fatalf("ordinary failure notice created %d job rows", jobs)
	}
	var gotStatus, gotReceipt, channel, addressKey, sessionID, payload string
	err = db.QueryRowContext(t.Context(), `SELECT status,COALESCE(provider_message_id,''),channel,address_key,session_id,payload FROM execution_delivery_outbox WHERE delivery_key=?`, notice.DedupeKey).Scan(&gotStatus, &gotReceipt, &channel, &addressKey, &sessionID, &payload)
	if err != nil {
		t.Fatal(err)
	}
	if gotStatus != status || gotReceipt != receipt || channel != locator.ChannelType || addressKey != locator.AddressKey || sessionID != locator.SessionID || payload != notice.Payload.String() {
		t.Fatalf("notice=%s durable status=%s receipt=%q locator=%s:%s session=%s", notice.ID, gotStatus, gotReceipt, channel, addressKey, sessionID)
	}
}

type terminalNoticeTransport struct {
	deliveries []deliveryworkflow.Delivery
	err        error
}

func (d *terminalNoticeTransport) Dispatch(_ context.Context, delivery deliveryworkflow.Delivery) (deliverycmd.Result, error) {
	d.deliveries = append(d.deliveries, delivery)
	if d.err != nil {
		return deliverycmd.Result{}, d.err
	}
	return deliverycmd.Result{ProviderMessageID: fmt.Sprintf("fake-telegram-message-%d", len(d.deliveries))}, nil
}

// Fail only the terminal notice: earlier progress publication is a separate
// command and must not consume this fault injection.
type terminalNoticePublicationFailure struct {
	bus       *recordingHandlerCommandBus
	err       error
	attempted actorlayer.Envelope
}

func (d *terminalNoticePublicationFailure) Dispatch(ctx context.Context, env actorlayer.Envelope) (*actortransport.DispatchReceipt, error) {
	var payload deliverycmd.Payload
	if err := actorlayer.UnmarshalPayload(env.Payload, &payload); err != nil {
		return nil, err
	}
	if payload.Settlement == deliverycmd.SettlementOutbox && payload.Text == terminalFailureMessage {
		d.attempted = env
		return nil, d.err
	}
	return d.bus.Dispatch(ctx, env)
}

func TestTerminalFailureNoticeExcludesCanceledSilentAutomaticExecution(t *testing.T) {
	for _, test := range []struct {
		name    string
		cause   error
		deliver bool
		source  string
		settles bool
	}{
		{"canceled", context.Canceled, true, turncmd.SourceTelegram, true},
		{"silent", errTerminalIterator, false, turncmd.SourceTelegram, false},
		{"automatic", errTerminalIterator, true, turncmd.SourceAuto, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			actor, bus, payload := executionFailureActor(t, func(adkagent.InvocationContext) iter.Seq2[*adksession.Event, error] {
				return func(yield func(*adksession.Event, error) bool) { calls++; yield(nil, test.cause) }
			})
			payload.Deliver, payload.Source = test.deliver, test.source
			env, err := turncmd.SessionTurnEnvelope(payload)
			if err != nil {
				t.Fatal(err)
			}
			env.Attempt, env.MaxAttempts = 4, 5
			err = actor.Handle(t.Context(), env)
			if test.settles && err != nil {
				t.Fatalf("canceled turn outcome=%v,want settled", err)
			}
			if !test.settles && !errors.Is(err, test.cause) {
				t.Fatalf("outcome=%v,want iterator cause", err)
			}
			if calls != 1 || len(terminalNotices(t, bus)) != 0 {
				t.Fatalf("provider calls=%d terminalnotices=%d,want1/0", calls, len(terminalNotices(t, bus)))
			}
		})
	}
}
